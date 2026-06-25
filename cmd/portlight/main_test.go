package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ichuan/portlight/internal/client"
	"github.com/ichuan/portlight/internal/server"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "portlight") {
		t.Fatalf("stdout = %q, want portlight version", stdout.String())
	}
}

func TestRunExposeHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"expose", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Usage:", "--server", "--port", "--json"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help output missing %q:\n%s", want, out)
		}
	}
}

func TestRunMissingCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code == 0 {
		t.Fatal("exit = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown command "bogus"`) {
		t.Fatalf("stderr = %q, want unknown command", stderr.String())
	}
}

func TestRunServerRejectsInvalidConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"server", "--public-base", "ftp://preview.example.com", "--token", "secret"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unsupported public base scheme") {
		t.Fatalf("stderr = %q, want unsupported scheme", stderr.String())
	}
}

func TestRunExposeRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing port",
			args: []string{"expose", "--server", "http://example.com", "--token", "secret"},
			want: "valid port required",
		},
		{
			name: "unsupported server scheme",
			args: []string{"expose", "--server", "ftp://example.com", "--token", "secret", "--port", "3000"},
			want: `unsupported server URL scheme "ftp"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("exit = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.want)
			}
		})
	}
}

func TestRunExposeJSONNameConflict(t *testing.T) {
	app, err := server.New(server.Config{
		PublicBase:          "https://preview.example.com",
		Token:               "secret",
		MaxTunnels:          4,
		MaxWorkersPerTunnel: 2,
		RequestTimeout:      time.Second,
		RandomName:          func() (string, error) { return "random", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	tunnel := httptest.NewServer(app.Handler())
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
			Port:      1,
			Workers:   1,
			OnReady:   func(ready client.Ready) { readyCh <- ready },
		})
	}()
	select {
	case <-readyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first tunnel")
	}

	var stdout, stderr bytes.Buffer
	codeCh := make(chan int, 1)
	go func() {
		codeCh <- run([]string{
			"expose",
			"--server", tunnel.URL,
			"--token", "secret",
			"--name", "demo",
			"--port", "1",
			"--json",
		}, &stdout, &stderr)
	}()
	var code int
	select {
	case code = <-codeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for conflicting expose command")
	}
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%s", code, stderr.String())
	}
	var got map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON output %q: %v", stdout.String(), err)
	}
	if got["status"] != "error" || got["error"] != "name_in_use" || got["name"] != "demo" {
		t.Fatalf("json = %#v, want name_in_use for demo", got)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			t.Fatalf("first Expose returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first Expose to stop")
	}
}
