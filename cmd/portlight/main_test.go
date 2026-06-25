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
	for _, want := range []string{"Usage:", "--server", "--port", "--ttl", "--json"} {
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

func TestRunSkill(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"skill"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"PORTLIGHT_TOKEN",
		"Copy-paste prompt",
		"full agent guide",
		"portlight expose --port",
		"--json",
		"trap",
		"TTL",
		"--ttl",
		"timeout",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("skill output missing %q:\n%s", want, out)
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

func TestRunExposeRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		want     string
		wantCode int
	}{
		{
			name:     "missing port",
			args:     []string{"expose", "--server", "http://example.com", "--token", "secret"},
			want:     "valid port required",
			wantCode: 1,
		},
		{
			name:     "unsupported server scheme",
			args:     []string{"expose", "--server", "ftp://example.com", "--token", "secret", "--port", "3000"},
			want:     `unsupported server URL scheme "ftp"`,
			wantCode: 1,
		},
		{
			name:     "negative ttl",
			args:     []string{"expose", "--server", "http://example.com", "--token", "secret", "--port", "3000", "--ttl", "-1s"},
			want:     "ttl must be positive",
			wantCode: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.want)
			}
		})
	}
}

func TestRunExposeTTLStopsTunnel(t *testing.T) {
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

	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := run([]string{
		"expose",
		"--server", tunnel.URL,
		"--token", "secret",
		"--port", "1",
		"--ttl", "50ms",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ttl expose took %s, want short exit", elapsed)
	}
	var ready client.Ready
	if err := json.Unmarshal(stdout.Bytes(), &ready); err != nil {
		t.Fatalf("invalid JSON output %q: %v", stdout.String(), err)
	}
	if ready.Status != "ready" || ready.Name != "random" || ready.URL == "" {
		t.Fatalf("ready = %#v", ready)
	}
}

func TestRunExposeTextOutput(t *testing.T) {
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

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"expose",
		"--server", tunnel.URL,
		"--token", "secret",
		"--port", "3000",
		"--ttl", "50ms",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"Exposed http://127.0.0.1:3000 as https://random.preview.example.com",
		"Press Ctrl+C to stop.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
}

func TestRunUpdateCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest.json" {
			t.Fatalf("path = %q, want /releases/latest.json", r.URL.Path)
		}
		fmt.Fprint(w, `{"version":"0.1.1","files":[{"os":"windows","arch":"amd64","url":"/downloads/portlight.exe","sha256":"abc"}]}`)
	}))
	defer srv.Close()
	restore := setUpdateTestHooks(t, "windows", "amd64", filepath.Join(t.TempDir(), "portlight.exe"))
	defer restore()

	var stdout, stderr bytes.Buffer
	code := run([]string{"update", "--server", srv.URL, "--check"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "0.1.1") || !strings.Contains(stdout.String(), "available") {
		t.Fatalf("stdout = %q, want update available", stdout.String())
	}
}

func TestRunUpdateDownloadsAndApplies(t *testing.T) {
	payload := []byte("new-binary")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest.json":
			fmt.Fprintf(w, `{"version":"0.1.1","files":[{"os":"windows","arch":"amd64","url":"/downloads/portlight.exe","sha256":"%x"}]}`, sum)
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

func TestRunUpdateSkipsWhenBinaryAlreadyMatchesLatestHash(t *testing.T) {
	oldVersion := version
	version = "dev"
	defer func() { version = oldVersion }()

	payload := []byte("latest-binary")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest.json":
			fmt.Fprintf(w, `{"version":"0.1.1","files":[{"os":"windows","arch":"amd64","url":"/downloads/portlight.exe","sha256":"%x"}]}`, sum)
		case "/downloads/portlight.exe":
			t.Fatal("download endpoint should not be requested")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	exe := filepath.Join(t.TempDir(), "portlight.exe")
	if err := os.WriteFile(exe, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	restore := setUpdateTestHooks(t, "windows", "amd64", exe)
	defer restore()

	var stdout, stderr bytes.Buffer
	code := run([]string{"update", "--server", srv.URL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "portlight 0.1.1 is up to date") {
		t.Fatalf("stdout = %q, want latest up-to-date message", stdout.String())
	}
}

func TestRunUninstallRemovesExecutable(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "portlight")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	restore := setUninstallTestHooks(t, "linux", exe)
	defer restore()

	var stdout, stderr bytes.Buffer
	code := run([]string{"uninstall"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(exe); !os.IsNotExist(err) {
		t.Fatalf("executable still exists or unexpected stat error: %v", err)
	}
	if !strings.Contains(stdout.String(), "removed "+exe) {
		t.Fatalf("stdout = %q, want removed path", stdout.String())
	}
}

func TestRunUninstallRejectsUnexpectedExecutableName(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	restore := setUninstallTestHooks(t, "linux", exe)
	defer restore()

	var stdout, stderr bytes.Buffer
	code := run([]string{"uninstall"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("executable removed or stat failed: %v", err)
	}
	if !strings.Contains(stderr.String(), "refusing to uninstall unexpected executable") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunUninstallWindowsSchedulesRemovalAndCleansPath(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "portlight.exe")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	restore := setUninstallTestHooks(t, "windows", exe)
	defer restore()

	var cleanedDir, scheduledTarget, scheduledDir string
	uninstallCleanupPath = func(dir string) error {
		cleanedDir = dir
		return nil
	}
	uninstallSchedule = func(target, dir string) error {
		scheduledTarget = target
		scheduledDir = dir
		return nil
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"uninstall"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if cleanedDir != filepath.Dir(exe) || scheduledTarget != exe || scheduledDir != filepath.Dir(exe) {
		t.Fatalf("cleanedDir=%q scheduledTarget=%q scheduledDir=%q", cleanedDir, scheduledTarget, scheduledDir)
	}
	if !strings.Contains(stdout.String(), "scheduled uninstall") {
		t.Fatalf("stdout = %q, want scheduled uninstall", stdout.String())
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

func setUninstallTestHooks(t *testing.T, goos, exe string) func() {
	t.Helper()
	oldGOOS := uninstallGOOS
	oldExecutable := uninstallExecutable
	oldRemove := uninstallRemove
	oldCleanupPath := uninstallCleanupPath
	oldSchedule := uninstallSchedule
	uninstallGOOS = goos
	uninstallExecutable = func() (string, error) { return exe, nil }
	uninstallRemove = os.Remove
	uninstallCleanupPath = cleanupWindowsUserPath
	uninstallSchedule = scheduleWindowsUninstall
	return func() {
		uninstallGOOS = oldGOOS
		uninstallExecutable = oldExecutable
		uninstallRemove = oldRemove
		uninstallCleanupPath = oldCleanupPath
		uninstallSchedule = oldSchedule
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
