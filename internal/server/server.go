package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var (
	ErrNameInUse      = errors.New("name_in_use")
	ErrInvalidName    = errors.New("invalid_name")
	ErrTooManyTunnels = errors.New("too_many_tunnels")
)

type Config struct {
	PublicBase          string
	Token               string
	MaxTunnels          int
	MaxWorkersPerTunnel int
	RequestTimeout      time.Duration
	RandomName          func() (string, error)
}

type App struct {
	cfg      Config
	baseHost string
	registry *registry
}

func New(cfg Config) (*App, error) {
	if cfg.Token == "" {
		return nil, errors.New("token required")
	}
	base, err := url.Parse(cfg.PublicBase)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid public base: %q", cfg.PublicBase)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("unsupported public base scheme: %q", base.Scheme)
	}
	if cfg.MaxTunnels <= 0 {
		cfg.MaxTunnels = 64
	}
	if cfg.MaxWorkersPerTunnel <= 0 {
		cfg.MaxWorkersPerTunnel = 8
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.RandomName == nil {
		cfg.RandomName = randomName
	}
	return &App{
		cfg:      cfg,
		baseHost: stripPort(strings.ToLower(base.Host)),
		registry: newRegistry(cfg),
	}, nil
}

func (a *App) Handler() http.Handler {
	return a
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz" || r.URL.Path == "/readyz":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case r.URL.Path == "/_control/open":
		a.handleOpen(w, r)
	case r.URL.Path == "/_control/worker":
		a.handleWorker(w, r)
	default:
		a.handleIngress(w, r)
	}
}

func (a *App) handleOpen(w http.ResponseWriter, r *http.Request) {
	if !authorized(r, a.cfg.Token) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tun, err := a.registry.open(r.URL.Query().Get("name"))
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidName):
			writeError(w, http.StatusBadRequest, "invalid_name")
		case errors.Is(err, ErrNameInUse):
			writeError(w, http.StatusConflict, "name_in_use")
		case errors.Is(err, ErrTooManyTunnels):
			writeError(w, http.StatusServiceUnavailable, "too_many_tunnels")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error")
		}
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		a.registry.close(tun.name)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	defer a.registry.close(tun.name)

	ready := map[string]string{
		"status": "ready",
		"name":   tun.name,
		"url":    a.publicURL(tun.name),
	}
	data, _ := json.Marshal(ready)
	if err := conn.Write(r.Context(), websocket.MessageText, data); err != nil {
		return
	}
	for {
		if _, _, err := conn.Read(r.Context()); err != nil {
			return
		}
	}
}

func (a *App) handleWorker(w http.ResponseWriter, r *http.Request) {
	if !authorized(r, a.cfg.Token) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	name := r.URL.Query().Get("name")
	tun := a.registry.get(name)
	if tun == nil {
		writeError(w, http.StatusNotFound, "tunnel_not_found")
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	netConn := websocket.NetConn(context.Background(), conn, websocket.MessageBinary)
	worker := &workerConn{conn: netConn, done: make(chan struct{})}
	if !tun.addWorker(worker) {
		_ = netConn.Close()
		_ = conn.Close(websocket.StatusTryAgainLater, "worker queue full")
		return
	}
	select {
	case <-worker.done:
	case <-tun.done:
	case <-r.Context().Done():
	}
	_ = netConn.Close()
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func (a *App) handleIngress(w http.ResponseWriter, r *http.Request) {
	if isWebSocketUpgrade(r) {
		writeError(w, http.StatusNotImplemented, "websocket_not_supported")
		return
	}
	name := a.nameFromHost(r.Host)
	if name == "" {
		writeError(w, http.StatusNotFound, "tunnel_not_found")
		return
	}
	tun := a.registry.get(name)
	if tun == nil {
		writeError(w, http.StatusNotFound, "tunnel_not_found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.RequestTimeout)
	defer cancel()
	worker, ok := tun.acquireWorker(ctx)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "worker_unavailable")
		return
	}
	defer worker.close()

	if err := r.Write(worker.conn); err != nil {
		writeError(w, http.StatusServiceUnavailable, "worker_unavailable")
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(worker.conn), r)
	if err != nil {
		writeError(w, http.StatusBadGateway, "bad_gateway")
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (a *App) publicURL(name string) string {
	base := strings.TrimRight(a.cfg.PublicBase, "/")
	u, _ := url.Parse(base)
	u.Host = name + "." + u.Host
	return u.String()
}

func (a *App) nameFromHost(host string) string {
	host = strings.ToLower(stripPort(host))
	suffix := "." + a.baseHost
	if !strings.HasSuffix(host, suffix) {
		return ""
	}
	name := strings.TrimSuffix(host, suffix)
	if strings.Contains(name, ".") || ValidateName(name) != nil {
		return ""
	}
	return name
}

type registry struct {
	cfg     Config
	mu      sync.Mutex
	tunnels map[string]*Tunnel
}

type Tunnel struct {
	name    string
	workers chan *workerConn
	done    chan struct{}
	once    sync.Once
}

type workerConn struct {
	conn net.Conn
	done chan struct{}
	once sync.Once
}

func newRegistry(cfg Config) *registry {
	return &registry{cfg: cfg, tunnels: map[string]*Tunnel{}}
}

func (r *registry) open(requested string) (*Tunnel, error) {
	if requested != "" {
		if err := ValidateName(requested); err != nil {
			return nil, err
		}
		return r.openName(requested)
	}
	for i := 0; i < 20; i++ {
		name, err := r.cfg.RandomName()
		if err != nil {
			return nil, err
		}
		if tun, err := r.openName(name); err == nil {
			return tun, nil
		} else if !errors.Is(err, ErrNameInUse) {
			return nil, err
		}
	}
	return nil, ErrNameInUse
}

func (r *registry) openName(name string) (*Tunnel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.tunnels) >= r.cfg.MaxTunnels {
		return nil, ErrTooManyTunnels
	}
	if _, exists := r.tunnels[name]; exists {
		return nil, ErrNameInUse
	}
	tun := &Tunnel{
		name:    name,
		workers: make(chan *workerConn, r.cfg.MaxWorkersPerTunnel),
		done:    make(chan struct{}),
	}
	r.tunnels[name] = tun
	return tun, nil
}

func (r *registry) get(name string) *Tunnel {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tunnels[name]
}

func (r *registry) close(name string) {
	r.mu.Lock()
	tun := r.tunnels[name]
	if tun != nil {
		delete(r.tunnels, name)
	}
	r.mu.Unlock()
	if tun != nil {
		tun.close()
	}
}

func (t *Tunnel) addWorker(w *workerConn) bool {
	select {
	case <-t.done:
		return false
	case t.workers <- w:
		return true
	default:
		return false
	}
}

func (t *Tunnel) acquireWorker(ctx context.Context) (*workerConn, bool) {
	select {
	case <-t.done:
		return nil, false
	case worker := <-t.workers:
		return worker, true
	case <-ctx.Done():
		return nil, false
	}
}

func (t *Tunnel) close() {
	t.once.Do(func() {
		close(t.done)
		for {
			select {
			case worker := <-t.workers:
				worker.close()
			default:
				return
			}
		}
	})
}

func (w *workerConn) close() {
	w.once.Do(func() {
		_ = w.conn.Close()
		close(w.done)
	})
}

var nameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,46}[a-z0-9])$`)

func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}

func randomName() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b [8]byte
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b[:]), nil
}

func authorized(r *http.Request, token string) bool {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	got := strings.TrimPrefix(value, prefix)
	if len(got) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHop(key) {
			continue
		}
		for _, value := range values {
			value = sanitizeIngressResponseHeader(key, value)
			if value == "" {
				continue
			}
			dst.Add(key, value)
		}
	}
}

func sanitizeIngressResponseHeader(key, value string) string {
	switch strings.ToLower(key) {
	case "x-frame-options":
		return ""
	case "content-security-policy":
		return stripFrameAncestors(value)
	default:
		return value
	}
}

func stripFrameAncestors(policy string) string {
	var kept []string
	for _, part := range strings.Split(policy, ";") {
		directive := strings.TrimSpace(part)
		if directive == "" {
			continue
		}
		name := directive
		if i := strings.IndexAny(directive, " \t\r\n"); i >= 0 {
			name = directive[:i]
		}
		if strings.EqualFold(name, "frame-ancestors") {
			continue
		}
		kept = append(kept, directive)
	}
	return strings.Join(kept, "; ")
}

func isHopByHop(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}
