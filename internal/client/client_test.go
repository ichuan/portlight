package client_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ichuan/portlight/internal/client"
	"github.com/ichuan/portlight/internal/server"
)

func TestExposeProxiesHTTP(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Portlight-Test", "ok")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "local:"+r.URL.Path+"?"+r.URL.RawQuery)
	}))
	defer local.Close()
	localHost, localPort := localAddress(t, local.URL)

	tunnel := newTunnelServer(t)
	defer tunnel.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyCh := make(chan client.Ready, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Expose(ctx, client.Config{
			ServerURL: tunnel.URL,
			Token:     "secret",
			Name:      "demo",
			LocalHost: localHost,
			Port:      localPort,
			Workers:   4,
			OnReady:   func(ready client.Ready) { readyCh <- ready },
		})
	}()

	ready := waitReady(t, readyCh)
	if ready.Status != "ready" || ready.Name != "demo" || ready.Target != local.URL {
		t.Fatalf("ready = %#v", ready)
	}

	req, err := http.NewRequest(http.MethodGet, tunnel.URL+"/hello?x=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "demo.preview.example.com"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", res.StatusCode, body)
	}
	if got := res.Header.Get("X-Portlight-Test"); got != "ok" {
		t.Fatalf("header = %q, want ok", got)
	}
	if string(body) != "local:/hello?x=1" {
		t.Fatalf("body = %q", body)
	}

	cancel()
	if err := <-errCh; err != nil && err != context.Canceled {
		t.Fatalf("Expose returned %v", err)
	}
}

func TestExposeUsesAssignedRandomNameForWorkers(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "random:"+r.URL.Path)
	}))
	defer local.Close()
	localHost, localPort := localAddress(t, local.URL)

	tunnel := newTunnelServer(t)
	defer tunnel.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyCh := make(chan client.Ready, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Expose(ctx, client.Config{
			ServerURL: tunnel.URL,
			Token:     "secret",
			LocalHost: localHost,
			Port:      localPort,
			Workers:   2,
			OnReady:   func(ready client.Ready) { readyCh <- ready },
		})
	}()

	ready := waitReady(t, readyCh)
	if ready.Status != "ready" || ready.Name != "random" || ready.Target != local.URL {
		t.Fatalf("ready = %#v", ready)
	}

	req, err := http.NewRequest(http.MethodGet, tunnel.URL+"/assigned", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "random.preview.example.com"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", res.StatusCode, body)
	}
	if string(body) != "random:/assigned" {
		t.Fatalf("body = %q", body)
	}

	cancel()
	if err := <-errCh; err != nil && err != context.Canceled {
		t.Fatalf("Expose returned %v", err)
	}
}

func TestExposeReportsNameConflictAndReleasesNameOnCancel(t *testing.T) {
	tunnel := newTunnelServer(t)
	defer tunnel.Close()

	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer local.Close()
	host, port := localAddress(t, local.URL)

	ctx, cancel := context.WithCancel(context.Background())
	readyCh := make(chan client.Ready, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Expose(ctx, client.Config{
			ServerURL: tunnel.URL,
			Token:     "secret",
			Name:      "demo",
			LocalHost: host,
			Port:      port,
			Workers:   1,
			OnReady:   func(ready client.Ready) { readyCh <- ready },
		})
	}()
	_ = waitReady(t, readyCh)

	conflict := client.Expose(context.Background(), client.Config{
		ServerURL: tunnel.URL,
		Token:     "secret",
		Name:      "demo",
		LocalHost: host,
		Port:      port,
		Workers:   1,
	})
	if !client.IsNameInUse(conflict) {
		t.Fatalf("conflict error = %v, want name_in_use", conflict)
	}

	cancel()
	if err := <-errCh; err != nil && err != context.Canceled {
		t.Fatalf("Expose returned %v", err)
	}

	released := make(chan client.Ready, 1)
	retryCtx, retryCancel := context.WithCancel(context.Background())
	defer retryCancel()
	retryErr := make(chan error, 1)
	go func() {
		retryErr <- client.Expose(retryCtx, client.Config{
			ServerURL: tunnel.URL,
			Token:     "secret",
			Name:      "demo",
			LocalHost: host,
			Port:      port,
			Workers:   1,
			OnReady:   func(ready client.Ready) { released <- ready },
		})
	}()
	_ = waitReady(t, released)
	retryCancel()
	if err := <-retryErr; err != nil && err != context.Canceled {
		t.Fatalf("retry returned %v", err)
	}
}

func TestExposeReturnsBadGatewayWhenUpstreamUnavailable(t *testing.T) {
	tunnel := newTunnelServer(t)
	defer tunnel.Close()

	port := unusedPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyCh := make(chan client.Ready, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Expose(ctx, client.Config{
			ServerURL: tunnel.URL,
			Token:     "secret",
			Name:      "demo",
			LocalHost: "127.0.0.1",
			Port:      port,
			Workers:   1,
			OnReady:   func(ready client.Ready) { readyCh <- ready },
		})
	}()
	_ = waitReady(t, readyCh)

	req, err := http.NewRequest(http.MethodGet, tunnel.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "demo.preview.example.com"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, want 502; body=%s", res.StatusCode, body)
	}

	cancel()
	if err := <-errCh; err != nil && err != context.Canceled {
		t.Fatalf("Expose returned %v", err)
	}
}

func newTunnelServer(t *testing.T) *httptest.Server {
	t.Helper()
	app, err := server.New(server.Config{
		PublicBase:          "https://preview.example.com",
		Token:               "secret",
		MaxTunnels:          8,
		MaxWorkersPerTunnel: 8,
		RequestTimeout:      time.Second,
		RandomName:          func() (string, error) { return "random", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(app.Handler())
}

func waitReady(t *testing.T, readyCh <-chan client.Ready) client.Ready {
	t.Helper()
	select {
	case ready := <-readyCh:
		return ready
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ready")
	}
	return client.Ready{}
}

func localAddress(t *testing.T, raw string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(req.URL.Host)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatal(err)
	}
	return host, port
}

func unusedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
