package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestRunExposeHelpShowsDefaultServer(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"expose", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), defaultServerURL) {
		t.Fatalf("help output missing default server %q:\n%s", defaultServerURL, stdout.String())
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

func TestRunUpdateCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest.json" {
			t.Fatalf("path = %q, want /releases/latest.json", r.URL.Path)
		}
		fmt.Fprint(w, `{"version":"0.2.0","files":[{"os":"windows","arch":"amd64","url":"/downloads/portlight.exe","sha256":"abc"}]}`)
	}))
	defer srv.Close()
	restore := setUpdateTestHooks(t, "windows", "amd64", filepath.Join(t.TempDir(), "portlight.exe"))
	defer restore()

	var stdout, stderr bytes.Buffer
	code := run([]string{"update", "--server", srv.URL, "--check"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "0.2.0") || !strings.Contains(stdout.String(), "available") {
		t.Fatalf("stdout = %q, want update available", stdout.String())
	}
}

func TestRunUpdateDownloadsAndApplies(t *testing.T) {
	payload := []byte("new-binary")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest.json":
			fmt.Fprintf(w, `{"version":"0.2.0","files":[{"os":"windows","arch":"amd64","url":"/downloads/portlight.exe","sha256":"%x"}]}`, sum)
		case "/downloads/portlight.exe":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	exe := filepath.Join(t.TempDir(), "portlight.exe")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	restore := setUpdateTestHooks(t, "windows", "amd64", exe)
	defer restore()

	var stdout, stderr bytes.Buffer
	code := run([]string{"update", "--server", srv.URL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(payload) {
		t.Fatalf("updated file = %q", data)
	}
	if !strings.Contains(stdout.String(), "updated portlight") {
		t.Fatalf("stdout = %q, want updated message", stdout.String())
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

func setUpdateTestHooks(t *testing.T, goos, goarch, exe string) func() {
	t.Helper()
	oldGOOS := updateGOOS
	oldGOARCH := updateGOARCH
	oldExecutable := updateExecutable
	oldApply := updateApply
	updateGOOS = goos
	updateGOARCH = goarch
	updateExecutable = func() (string, error) { return exe, nil }
	updateApply = func(downloaded, target string) error {
		data, err := os.ReadFile(downloaded)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o755)
	}
	return func() {
		updateGOOS = oldGOOS
		updateGOARCH = oldGOARCH
		updateExecutable = oldExecutable
		updateApply = oldApply
	}
}
