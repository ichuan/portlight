package update

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckOnlyFindsMatchingPlatform(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != LatestPath {
			t.Fatalf("path = %q, want %s", r.URL.Path, LatestPath)
		}
		fmt.Fprint(w, `{
			"version": "0.1.1",
			"files": [
				{"os": "linux", "arch": "arm64", "url": "/downloads/other", "sha256": "abc"},
				{"os": "linux", "arch": "amd64", "url": "/downloads/portlight-linux-amd64", "sha256": "def"}
			]
		}`)
	}))
	defer srv.Close()

	result, err := Run(context.Background(), Config{
		ServerURL:      srv.URL,
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		CheckOnly:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.LatestVersion != "0.1.1" || result.URL != srv.URL+"/downloads/portlight-linux-amd64" {
		t.Fatalf("result = %#v", result)
	}
	if result.AlreadyLatest || result.Updated {
		t.Fatalf("result = %#v, want update available only", result)
	}
}

func TestRunSkipsAlreadyLatestVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"0.1.1","files":[{"os":"linux","arch":"amd64","url":"/downloads/portlight","sha256":"abc"}]}`)
	}))
	defer srv.Close()

	called := false
	result, err := Run(context.Background(), Config{
		ServerURL:      srv.URL,
		CurrentVersion: "0.1.1",
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecutablePath: filepath.Join(t.TempDir(), "portlight"),
		Apply: func(_, _ string) error {
			called = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyLatest || result.Updated || called {
		t.Fatalf("result = %#v, apply called=%v", result, called)
	}
}

func TestRunSkipsWhenExecutableAlreadyMatchesLatestHash(t *testing.T) {
	payload := []byte("latest-binary")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case LatestPath:
			fmt.Fprintf(w, `{"version":"0.1.1","files":[{"os":"linux","arch":"amd64","url":"/downloads/portlight","sha256":"%x"}]}`, sum)
		case "/downloads/portlight":
			t.Fatal("download endpoint should not be requested")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	exe := filepath.Join(t.TempDir(), "portlight")
	if err := os.WriteFile(exe, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	result, err := Run(context.Background(), Config{
		ServerURL:      srv.URL,
		CurrentVersion: "dev",
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecutablePath: exe,
		Apply: func(_, _ string) error {
			called = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyLatest || result.Updated || called {
		t.Fatalf("result = %#v, apply called=%v", result, called)
	}
}

func TestRunDownloadsVerifiesAndApplies(t *testing.T) {
	payload := []byte("new-binary")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case LatestPath:
			fmt.Fprintf(w, `{"version":"0.1.1","files":[{"os":"linux","arch":"amd64","url":"/downloads/portlight","sha256":"%x"}]}`, sum)
		case "/downloads/portlight":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	exe := filepath.Join(t.TempDir(), "portlight")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var appliedBytes []byte
	result, err := Run(context.Background(), Config{
		ServerURL:      srv.URL,
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecutablePath: exe,
		Apply: func(downloaded, target string) error {
			if target != exe {
				t.Fatalf("target = %q, want %q", target, exe)
			}
			data, err := os.ReadFile(downloaded)
			if err != nil {
				return err
			}
			appliedBytes = data
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.LatestVersion != "0.1.1" {
		t.Fatalf("result = %#v", result)
	}
	if string(appliedBytes) != string(payload) {
		t.Fatalf("applied bytes = %q", appliedBytes)
	}
}

func TestRunRejectsChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case LatestPath:
			fmt.Fprint(w, `{"version":"0.1.1","files":[{"os":"linux","arch":"amd64","url":"/downloads/portlight","sha256":"0000"}]}`)
		case "/downloads/portlight":
			_, _ = w.Write([]byte("new-binary"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	called := false
	_, err := Run(context.Background(), Config{
		ServerURL:      srv.URL,
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecutablePath: filepath.Join(t.TempDir(), "portlight"),
		Apply: func(_, _ string) error {
			called = true
			return nil
		},
	})
	if err == nil {
		t.Fatal("Run succeeded, want checksum error")
	}
	if called {
		t.Fatal("apply called despite checksum mismatch")
	}
}
