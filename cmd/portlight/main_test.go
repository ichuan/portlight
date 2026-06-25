package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestRunServeHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"serve", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Usage:", "--dir", "--port", "--ttl", "--json", "--include-hidden"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help output missing %q:\n%s", want, out)
		}
	}
}

func TestFilteredFileSystemListsVisibleFilesAndBlocksHidden(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(filteredFileSystem{root: root}))
	defer srv.Close()

	body := httpGetString(t, srv.URL+"/")
	if !strings.Contains(body, "visible.txt") {
		t.Fatalf("listing missing visible file:\n%s", body)
	}
	if strings.Contains(body, ".env") {
		t.Fatalf("listing exposed hidden file:\n%s", body)
	}
	res, err := http.Get(srv.URL + "/.env")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden file status = %d, want 404", res.StatusCode)
	}
}

func TestFilteredFileSystemCanIncludeHidden(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(filteredFileSystem{root: root, includeHidden: true}))
	defer srv.Close()

	if body := httpGetString(t, srv.URL+"/"); !strings.Contains(body, ".env") {
		t.Fatalf("listing missing hidden file with includeHidden=true:\n%s", body)
	}
	if body := httpGetString(t, srv.URL+"/.env"); body != "secret" {
		t.Fatalf("hidden file body = %q, want secret", body)
	}
}

func TestFilteredFileSystemRejectsSymlinkToHiddenFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "visible-link")
	if err := os.Symlink(".env", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	srv := httptest.NewServer(http.FileServer(filteredFileSystem{root: root}))
	defer srv.Close()

	if body := httpGetString(t, srv.URL+"/"); strings.Contains(body, "visible-link") {
		t.Fatalf("listing exposed symlink:\n%s", body)
	}
	res, err := http.Get(srv.URL + "/visible-link")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("symlink status = %d, want 404", res.StatusCode)
	}
}

func TestRunServeRejectsSymlinkRoot(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, ".hidden-root")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "public")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"serve", "--dir", link, "--ttl", "1ns", "--token", "secret"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit = 0, want symlink root rejection; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestRunServePublishesDirectoryListingAndBlocksHiddenFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	tunnel := newReachableTunnel(t, server.Config{
		Token:               "secret",
		MaxTunnels:          4,
		MaxWorkersPerTunnel: 2,
		RequestTimeout:      time.Second,
		RandomName:          func() (string, error) { return "serve-test", nil },
	})
	defer tunnel.Close()

	var stdout lockedBuffer
	var stderr lockedBuffer
	codeCh := make(chan int, 1)
	go func() {
		codeCh <- run([]string{
			"serve",
			"--server", tunnel.URL,
			"--token", "secret",
			"--dir", root,
			"--ttl", "800ms",
			"--json",
		}, &stdout, &stderr)
	}()
	ready := waitReadyJSON(t, &stdout)
	publicURL, err := url.Parse(ready.URL)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodGet, tunnel.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = publicURL.Host
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("listing status = %d, body=%s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), "visible.txt") {
		t.Fatalf("listing missing visible file:\n%s", body)
	}
	if strings.Contains(string(body), ".env") {
		t.Fatalf("listing exposed hidden file:\n%s", body)
	}

	req, err = http.NewRequest(http.MethodGet, tunnel.URL+"/.env", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = publicURL.Host
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("hidden file status = %d, want 404", res.StatusCode)
	}

	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("exit = %d, stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for serve command to exit")
	}
}

func TestRunServeReturnsErrorWhenTTLExpiresBeforeReady(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"serve",
		"--server", "http://10.255.255.1",
		"--token", "secret",
		"--dir", root,
		"--ttl", "1ns",
		"--json",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit = 0, want non-zero before ready; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("stdout = %q, want no ready JSON", stdout.String())
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
		"portlight serve",
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

func httpGetString(t *testing.T, url string) string {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s status = %d, body=%s", url, res.StatusCode, body)
	}
	return string(body)
}

func newReachableTunnel(t *testing.T, cfg server.Config) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.PublicBase = "http://" + listener.Addr().String()
	app, err := server.New(cfg)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(app.Handler())
	srv.Listener = listener
	srv.Start()
	return srv
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func waitReadyJSON(t *testing.T, out *lockedBuffer) client.Ready {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var ready client.Ready
		if err := json.Unmarshal(out.Bytes(), &ready); err == nil && ready.Status == "ready" {
			return ready
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for ready JSON; stdout=%s", out.String())
	return client.Ready{}
}
