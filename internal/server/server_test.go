package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/ichuan/portlight/internal/server"
)

func TestValidateName(t *testing.T) {
	valid := []string{"abc", "myapp", "my-app-123", "a12"}
	for _, name := range valid {
		if err := server.ValidateName(name); err != nil {
			t.Fatalf("ValidateName(%q): %v", name, err)
		}
	}

	invalid := []string{"", "ab", "-abc", "abc-", "Aabc", "my_app", strings.Repeat("a", 49)}
	for _, name := range invalid {
		if err := server.ValidateName(name); err == nil {
			t.Fatalf("ValidateName(%q) succeeded, want error", name)
		}
	}
}

func TestControlRejectsNameInUseAndCleanupAllowsReuse(t *testing.T) {
	srv := newTestServer(t, server.Config{
		PublicBase:          "https://preview.example.com",
		Token:               "secret",
		MaxTunnels:          4,
		MaxWorkersPerTunnel: 2,
		RequestTimeout:      20 * time.Millisecond,
		RandomName:          func() (string, error) { return "random", nil },
	})
	defer srv.Close()

	first := dialControl(t, srv.URL, "secret", "demo")
	defer first.Close(websocket.StatusNormalClosure, "")

	resp := dialControlExpectHTTP(t, srv.URL, "secret", "demo")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}

	_ = first.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(time.Second)
	for {
		conn, resp, err := websocket.Dial(context.Background(), controlURL(t, srv.URL, "demo"), dialOptions("secret"))
		if err == nil {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		}
		if resp != nil && resp.StatusCode != http.StatusConflict {
			t.Fatalf("status after close = %d, want 101 or 409 while cleanup races", resp.StatusCode)
		}
		if time.Now().After(deadline) {
			t.Fatalf("name was not released after control close: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestControlRejectsUnauthorizedInvalidNameAndTooManyTunnels(t *testing.T) {
	srv := newTestServer(t, server.Config{
		PublicBase:          "https://preview.example.com",
		Token:               "secret",
		MaxTunnels:          1,
		MaxWorkersPerTunnel: 2,
		RequestTimeout:      20 * time.Millisecond,
		RandomName:          func() (string, error) { return "random", nil },
	})
	defer srv.Close()

	unauthorized := dialControlExpectHTTP(t, srv.URL, "wrong", "demo")
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorized.StatusCode)
	}

	invalid := dialControlExpectHTTP(t, srv.URL, "secret", "Bad_Name")
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid name status = %d, want 400", invalid.StatusCode)
	}

	first := dialControl(t, srv.URL, "secret", "demo")
	defer first.Close(websocket.StatusNormalClosure, "")

	tooMany := dialControlExpectHTTP(t, srv.URL, "secret", "other")
	if tooMany.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("too many status = %d, want 503", tooMany.StatusCode)
	}
}

func TestIngressErrors(t *testing.T) {
	srv := newTestServer(t, server.Config{
		PublicBase:          "https://preview.example.com",
		Token:               "secret",
		MaxTunnels:          4,
		MaxWorkersPerTunnel: 2,
		RequestTimeout:      20 * time.Millisecond,
		RandomName:          func() (string, error) { return "random", nil },
	})
	defer srv.Close()

	missing := hostRequest(t, srv.URL, "missing.preview.example.com", "/")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing tunnel status = %d, want 404", missing.StatusCode)
	}

	control := dialControl(t, srv.URL, "secret", "demo")
	defer control.Close(websocket.StatusNormalClosure, "")

	noWorker := hostRequest(t, srv.URL, "demo.preview.example.com", "/")
	if noWorker.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no worker status = %d, want 503", noWorker.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "demo.preview.example.com"
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	upgrade, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer upgrade.Body.Close()
	if upgrade.StatusCode != http.StatusNotImplemented {
		t.Fatalf("upgrade status = %d, want 501", upgrade.StatusCode)
	}
}

func TestWorkerRejectsUnauthorizedAndUnknownTunnel(t *testing.T) {
	srv := newTestServer(t, server.Config{
		PublicBase:          "https://preview.example.com",
		Token:               "secret",
		MaxTunnels:          4,
		MaxWorkersPerTunnel: 2,
		RequestTimeout:      20 * time.Millisecond,
		RandomName:          func() (string, error) { return "random", nil },
	})
	defer srv.Close()

	unauthorized := dialWorkerExpectHTTP(t, srv.URL, "wrong", "demo")
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized worker status = %d, want 401", unauthorized.StatusCode)
	}

	unknown := dialWorkerExpectHTTP(t, srv.URL, "secret", "missing")
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown worker status = %d, want 404", unknown.StatusCode)
	}
}

func TestControlRejectsCrossOriginBrowserRequest(t *testing.T) {
	srv := newTestServer(t, server.Config{
		PublicBase:          "https://preview.example.com",
		Token:               "secret",
		MaxTunnels:          4,
		MaxWorkersPerTunnel: 2,
		RequestTimeout:      20 * time.Millisecond,
		RandomName:          func() (string, error) { return "random", nil },
	})
	defer srv.Close()

	opts := dialOptions("secret")
	opts.HTTPHeader.Set("Origin", "https://evil.example")
	conn, resp, err := websocket.Dial(context.Background(), controlURL(t, srv.URL, "demo"), opts)
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
	if err == nil {
		t.Fatal("cross-origin dial succeeded, want HTTP error")
	}
	if resp == nil {
		t.Fatalf("missing HTTP response: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	allowed := dialControl(t, srv.URL, "secret", "demo")
	_ = allowed.Close(websocket.StatusNormalClosure, "")
}

func TestIngressProxiesThroughWorkerAndFiltersHopByHopResponseHeaders(t *testing.T) {
	srv := newTestServer(t, server.Config{
		PublicBase:          "https://preview.example.com",
		Token:               "secret",
		MaxTunnels:          4,
		MaxWorkersPerTunnel: 2,
		RequestTimeout:      time.Second,
		RandomName:          func() (string, error) { return "random", nil },
	})
	defer srv.Close()

	control := dialControl(t, srv.URL, "secret", "demo")
	defer control.CloseNow()

	workerCtx, workerCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer workerCancel()
	worker, _, err := websocket.Dial(workerCtx, workerURL(t, srv.URL, "demo"), dialOptions("secret"))
	if err != nil {
		t.Fatal(err)
	}
	defer worker.CloseNow()
	workerConn := websocket.NetConn(context.Background(), worker, websocket.MessageBinary)
	defer workerConn.Close()

	workerDone := make(chan error, 1)
	go func() {
		defer workerConn.Close()
		req, err := http.ReadRequest(bufio.NewReader(workerConn))
		if err != nil {
			workerDone <- err
			return
		}
		defer req.Body.Close()
		body, err := io.ReadAll(req.Body)
		if err != nil {
			workerDone <- err
			return
		}
		if req.Method != http.MethodPost || req.URL.Path != "/submit" || req.URL.RawQuery != "x=1" {
			workerDone <- &testError{msg: "unexpected proxied request line: " + req.Method + " " + req.URL.String()}
			return
		}
		if string(body) != "payload" {
			workerDone <- &testError{msg: "unexpected proxied body: " + string(body)}
			return
		}
		resp := &http.Response{
			StatusCode:    http.StatusAccepted,
			Status:        "202 Accepted",
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			ContentLength: int64(len("proxied")),
			Header: http.Header{
				"Connection":       {"close"},
				"X-Portlight-Test": {"ok"},
			},
			Body: io.NopCloser(strings.NewReader("proxied")),
		}
		workerDone <- resp.Write(workerConn)
	}()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/submit?x=1", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "demo.preview.example.com"
	httpClient := &http.Client{Timeout: 2 * time.Second}
	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", res.StatusCode, body)
	}
	if string(body) != "proxied" {
		t.Fatalf("body = %q, want proxied", body)
	}
	if got := res.Header.Get("X-Portlight-Test"); got != "ok" {
		t.Fatalf("X-Portlight-Test = %q, want ok", got)
	}
	if got := res.Header.Get("Connection"); got != "" {
		t.Fatalf("Connection header = %q, want filtered", got)
	}
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker response")
	}
}

func TestHealthAndReady(t *testing.T) {
	srv := newTestServer(t, server.Config{
		PublicBase:          "https://preview.example.com",
		Token:               "secret",
		MaxTunnels:          4,
		MaxWorkersPerTunnel: 2,
		RequestTimeout:      20 * time.Millisecond,
		RandomName:          func() (string, error) { return "random", nil },
	})
	defer srv.Close()

	for _, path := range []string{"/healthz", "/readyz"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, res.StatusCode)
		}
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  server.Config
	}{
		{
			name: "missing token",
			cfg:  server.Config{PublicBase: "https://preview.example.com"},
		},
		{
			name: "missing public base",
			cfg:  server.Config{Token: "secret"},
		},
		{
			name: "unsupported public base scheme",
			cfg:  server.Config{PublicBase: "ftp://preview.example.com", Token: "secret"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := server.New(tt.cfg); err == nil {
				t.Fatal("New succeeded, want error")
			}
		})
	}
}

func newTestServer(t *testing.T, cfg server.Config) *httptest.Server {
	t.Helper()
	app, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(app.Handler())
}

func dialControl(t *testing.T, serverURL, token, name string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), controlURL(t, serverURL, name), dialOptions(token))
	if err != nil {
		t.Fatal(err)
	}
	var ready struct {
		Status string `json:"status"`
		Name   string `json:"name"`
		URL    string `json:"url"`
	}
	_, data, err := conn.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Status != "ready" || ready.Name != name || ready.URL == "" {
		t.Fatalf("ready = %#v", ready)
	}
	return conn
}

func dialControlExpectHTTP(t *testing.T, serverURL, token, name string) *http.Response {
	t.Helper()
	conn, resp, err := websocket.Dial(context.Background(), controlURL(t, serverURL, name), dialOptions(token))
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
	if err == nil {
		t.Fatal("dial succeeded, want HTTP error")
	}
	if resp == nil {
		t.Fatalf("missing HTTP response: %v", err)
	}
	return resp
}

func dialWorkerExpectHTTP(t *testing.T, serverURL, token, name string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, workerURL(t, serverURL, name), dialOptions(token))
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
	if err == nil {
		t.Fatal("worker dial succeeded, want HTTP error")
	}
	if resp == nil {
		t.Fatalf("missing HTTP response: %v", err)
	}
	return resp
}

func controlURL(t *testing.T, serverURL, name string) string {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Scheme = "ws"
	u.Path = "/_control/open"
	q := u.Query()
	q.Set("name", name)
	u.RawQuery = q.Encode()
	return u.String()
}

func workerURL(t *testing.T, serverURL, name string) string {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Scheme = "ws"
	u.Path = "/_control/worker"
	q := u.Query()
	q.Set("name", name)
	u.RawQuery = q.Encode()
	return u.String()
}

func dialOptions(token string) *websocket.DialOptions {
	return &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}}
}

func hostRequest(t *testing.T, serverURL, host, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, serverURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
