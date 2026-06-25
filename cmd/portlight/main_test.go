package main

import (
	"bytes"
	"strings"
	"testing"
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
