package server_test

import (
	"context"
	"encoding/json"
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
