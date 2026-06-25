package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var ErrNameInUse = errors.New("name_in_use")

type Config struct {
	ServerURL string
	Token     string
	Name      string
	LocalHost string
	Port      int
	Workers   int
	OnReady   func(Ready)
}

type Ready struct {
	Status string `json:"status"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Target string `json:"target"`
}

type conflictError struct {
	name string
}

func (e conflictError) Error() string {
	if e.name == "" {
		return ErrNameInUse.Error()
	}
	return "name_in_use: " + e.name
}

func (e conflictError) Unwrap() error { return ErrNameInUse }

func IsNameInUse(err error) bool {
	return errors.Is(err, ErrNameInUse)
}

func Expose(ctx context.Context, cfg Config) error {
	if cfg.ServerURL == "" {
		return errors.New("server URL required")
	}
	if cfg.Token == "" {
		return errors.New("token required")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return errors.New("valid port required")
	}
	if cfg.LocalHost == "" {
		cfg.LocalHost = "127.0.0.1"
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 8
	}

	controlURL, err := controlURL(cfg.ServerURL, cfg.Name)
	if err != nil {
		return err
	}
	control, resp, err := websocket.Dial(ctx, controlURL, dialOptions(cfg.Token))
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusConflict {
			return conflictError{name: cfg.Name}
		}
		return err
	}
	defer control.Close(websocket.StatusNormalClosure, "")

	_, data, err := control.Read(ctx)
	if err != nil {
		return err
	}
	var ready Ready
	if err := json.Unmarshal(data, &ready); err != nil {
		return err
	}
	ready.Target = localTarget(cfg.LocalHost, cfg.Port)
	workerCfg := cfg
	workerCfg.Name = ready.Name

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	firstWorker := make(chan struct{})
	var firstWorkerOnce sync.Once
	for i := 0; i < cfg.Workers; i++ {
		go workerLoop(runCtx, workerCfg, func() {
			firstWorkerOnce.Do(func() { close(firstWorker) })
		})
	}
	select {
	case <-firstWorker:
	case <-time.After(500 * time.Millisecond):
	case <-runCtx.Done():
		return runCtx.Err()
	}
	if cfg.OnReady != nil {
		cfg.OnReady(ready)
	}

	controlErr := make(chan error, 1)
	go func() {
		for {
			if _, _, err := control.Read(runCtx); err != nil {
				controlErr <- err
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-controlErr:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
}

func workerLoop(ctx context.Context, cfg Config, onConnected func()) {
	for ctx.Err() == nil {
		err := handleWorker(ctx, cfg, onConnected)
		onConnected = nil
		if err != nil && ctx.Err() == nil {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func handleWorker(ctx context.Context, cfg Config, onConnected func()) error {
	workerURL, err := workerURL(cfg.ServerURL, cfg.Name)
	if err != nil {
		return err
	}
	ws, _, err := websocket.Dial(ctx, workerURL, dialOptions(cfg.Token))
	if err != nil {
		return err
	}
	netConn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer netConn.Close()
	if onConnected != nil {
		onConnected()
	}

	req, err := http.ReadRequest(bufio.NewReader(netConn))
	if err != nil {
		return err
	}
	defer req.Body.Close()

	resp, err := roundTripLocal(ctx, cfg, req)
	if err != nil {
		return writeGatewayError(netConn)
	}
	defer resp.Body.Close()
	return resp.Write(netConn)
}

func roundTripLocal(ctx context.Context, cfg Config, req *http.Request) (*http.Response, error) {
	target := &url.URL{
		Scheme:   "http",
		Host:     net.JoinHostPort(cfg.LocalHost, strconv.Itoa(cfg.Port)),
		Path:     req.URL.Path,
		RawQuery: req.URL.RawQuery,
	}
	out := &http.Request{
		Method:        req.Method,
		URL:           target,
		Header:        cloneHeaders(req.Header),
		Body:          req.Body,
		ContentLength: req.ContentLength,
		Host:          target.Host,
	}
	return http.DefaultTransport.RoundTrip(out.WithContext(ctx))
}

func cloneHeaders(src http.Header) http.Header {
	dst := http.Header{}
	for key, values := range src {
		if isHopByHop(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	return dst
}

func writeGatewayError(w io.Writer) error {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"bad_gateway"}` + "\n")),
	}
	return resp.Write(w)
}

func controlURL(serverURL, name string) (string, error) {
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	return websocketURL(serverURL, "/_control/open", q)
}

func workerURL(serverURL, name string) (string, error) {
	q := url.Values{}
	q.Set("name", name)
	return websocketURL(serverURL, "/_control/worker", q)
}

func websocketURL(serverURL, path string, query url.Values) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported server URL scheme %q", u.Scheme)
	}
	u.Path = path
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func dialOptions(token string) *websocket.DialOptions {
	return &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}}
}

func localTarget(host string, port int) string {
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func isHopByHop(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}
