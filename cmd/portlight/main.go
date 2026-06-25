package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ichuan/portlight/internal/client"
	"github.com/ichuan/portlight/internal/update"
)

var version = "dev"

const defaultServerURL = "https://portlight.616.pub"

var (
	updateGOOS       = runtime.GOOS
	updateGOARCH     = runtime.GOARCH
	updateExecutable = os.Executable
	updateApply      func(downloaded, target string) error

	uninstallGOOS        = runtime.GOOS
	uninstallExecutable  = os.Executable
	uninstallRemove      = os.Remove
	uninstallCleanupPath = cleanupWindowsUserPath
	uninstallSchedule    = scheduleWindowsUninstall
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "--version", "version":
		fmt.Fprintf(stdout, "portlight %s\n", version)
		return 0
	case "-h", "--help", "help":
		printUsage(stdout)
		return 0
	case "expose":
		return runExpose(args[1:], stdout, stderr)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "update":
		return runUpdate(args[1:], stdout, stderr)
	case "uninstall":
		return runUninstall(args[1:], stdout, stderr)
	case "skill":
		return runSkill(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runExpose(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("expose", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: portlight expose [options]")
		printFlagDefaults(stdout, fs)
	}
	serverURL := fs.String("server", defaultServerURL, "portlight server URL")
	token := fs.String("token", os.Getenv("PORTLIGHT_TOKEN"), "server token; defaults to PORTLIGHT_TOKEN")
	port := fs.Int("port", 0, "local HTTP port to expose")
	host := fs.String("host", "127.0.0.1", "local HTTP host")
	name := fs.String("name", "", "optional requested public subdomain name")
	workers := fs.Int("workers", 8, "number of worker connections")
	ttl := fs.Duration("ttl", 0, "optional tunnel lifetime, for example 5m or 1h")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	quiet := fs.Bool("quiet", false, "suppress non-JSON ready output")
	if err := fs.Parse(args); err != nil {
		return helpExitCode(err)
	}
	if *ttl < 0 {
		fmt.Fprintln(stderr, "ttl must be positive")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *ttl > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *ttl)
		defer cancel()
	}
	err := client.Expose(ctx, client.Config{
		ServerURL: *serverURL,
		Token:     *token,
		Name:      *name,
		LocalHost: *host,
		Port:      *port,
		Workers:   *workers,
		OnReady: func(ready client.Ready) {
			if *jsonOut {
				_ = json.NewEncoder(stdout).Encode(ready)
				return
			}
			if !*quiet {
				fmt.Fprintf(stdout, "Exposed %s as %s\n", ready.Target, ready.URL)
				fmt.Fprintln(stdout, "Press Ctrl+C to stop.")
			}
		},
	})
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0
	}
	if client.IsNameInUse(err) && *jsonOut {
		_ = json.NewEncoder(stdout).Encode(map[string]string{
			"status": "error",
			"error":  "name_in_use",
			"name":   *name,
		})
		return 1
	}
	fmt.Fprintln(stderr, err)
	return 1
}

func runUpdate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: portlight update [options]")
		printFlagDefaults(stdout, fs)
	}
	serverURL := fs.String("server", defaultServerURL, "portlight server URL")
	checkOnly := fs.Bool("check", false, "check for an update without installing it")
	if err := fs.Parse(args); err != nil {
		return helpExitCode(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	exe, err := updateExecutable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	result, err := update.Run(ctx, update.Config{
		ServerURL:      *serverURL,
		CurrentVersion: version,
		GOOS:           updateGOOS,
		GOARCH:         updateGOARCH,
		ExecutablePath: exe,
		CheckOnly:      *checkOnly,
		Apply:          updateApply,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch {
	case result.AlreadyLatest:
		displayVersion := result.CurrentVersion
		if result.LatestVersion != "" && result.CurrentVersion != result.LatestVersion {
			displayVersion = result.LatestVersion
		}
		fmt.Fprintf(stdout, "portlight %s is up to date\n", displayVersion)
	case result.CheckOnly:
		fmt.Fprintf(stdout, "portlight %s is available for %s/%s\n", result.LatestVersion, updateGOOS, updateGOARCH)
	default:
		fmt.Fprintf(stdout, "updated portlight from %s to %s\n", result.CurrentVersion, result.LatestVersion)
		if updateGOOS == "windows" {
			fmt.Fprintln(stdout, "restart your shell after the replacement completes")
		}
	}
	return 0
}

func runUninstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: portlight uninstall")
		printFlagDefaults(stdout, fs)
	}
	if err := fs.Parse(args); err != nil {
		return helpExitCode(err)
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected uninstall argument %q\n", fs.Arg(0))
		return 2
	}
	exe, err := uninstallExecutable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !isPortlightExecutable(exe) {
		fmt.Fprintf(stderr, "refusing to uninstall unexpected executable %q\n", exe)
		return 1
	}
	if uninstallGOOS == "windows" {
		dir := filepath.Dir(exe)
		if err := uninstallCleanupPath(dir); err != nil {
			fmt.Fprintf(stderr, "warning: failed to update user PATH: %v\n", err)
		}
		if err := uninstallSchedule(exe, dir); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "scheduled uninstall of %s\n", exe)
		fmt.Fprintln(stdout, "Restart your shell after uninstall completes.")
		return 0
	}
	if err := uninstallRemove(exe); err != nil {
		fmt.Fprintf(stderr, "failed to remove %s: %v\n", exe, err)
		if os.IsPermission(err) {
			fmt.Fprintln(stderr, "try running the command with elevated permissions")
		}
		return 1
	}
	cleanupUpdateTemps(filepath.Dir(exe))
	fmt.Fprintf(stdout, "removed %s\n", exe)
	return 0
}

func runSkill(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] != "-h" && args[0] != "--help" {
		fmt.Fprintf(stderr, "unknown skill option %q\n", args[0])
		_, _ = io.WriteString(stderr, skillGuide)
		return 2
	}
	_, _ = io.WriteString(stdout, skillGuide)
	return 0
}

func helpExitCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: portlight <command> [options]

Commands:
  expose   expose a local HTTP port
  serve    serve a directory and expose it
  update   update this portlight binary
  uninstall remove this portlight binary
  skill    print agent and CI usage guidance
  version  print version

Examples:
  portlight expose --port 3000
  portlight serve
  portlight update
  portlight uninstall
  portlight skill`)
}

func printFlagDefaults(w io.Writer, fs *flag.FlagSet) {
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	fs.SetOutput(w)
	text := strings.ReplaceAll(buf.String(), "  -", "  --")
	fmt.Fprint(w, text)
}

func isPortlightExecutable(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return name == "portlight" || name == "portlight.exe"
}

func cleanupUpdateTemps(dir string) {
	matches, err := filepath.Glob(filepath.Join(dir, ".portlight-update-*"))
	if err != nil {
		return
	}
	for _, path := range matches {
		_ = os.Remove(path)
	}
}

func cleanupWindowsUserPath(dir string) error {
	script := `$dir = [IO.Path]::GetFullPath($args[0]).TrimEnd('\')
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $userPath) { exit 0 }
$parts = @()
foreach ($part in ($userPath -split ';')) {
  if (-not $part) { continue }
  try {
    $full = [IO.Path]::GetFullPath($part).TrimEnd('\')
    if ([String]::Compare($full, $dir, $true) -ne 0) { $parts += $part }
  } catch {
    $parts += $part
  }
}
[Environment]::SetEnvironmentVariable('Path', ($parts -join ';'), 'User')`
	return exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script, dir).Run()
}

func scheduleWindowsUninstall(target, dir string) error {
	script, err := os.CreateTemp("", "portlight-uninstall-*.cmd")
	if err != nil {
		return err
	}
	scriptPath := script.Name()
	content := fmt.Sprintf(`@echo off
setlocal
set "target=%s"
set "dir=%s"
:retry
timeout /t 1 /nobreak >nul
del /f /q "%%target%%" >nul 2>nul
if exist "%%target%%" goto retry
rmdir "%%dir%%" >nul 2>nul
del "%%~f0"
`, target, dir)
	if _, err := script.WriteString(content); err != nil {
		_ = script.Close()
		_ = os.Remove(scriptPath)
		return err
	}
	if err := script.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return err
	}
	return exec.Command("cmd", "/C", "start", "", "/B", scriptPath).Start()
}

const skillGuide = `Portlight agent skill

Use portlight when a remote agent or CI job needs a public HTTPS URL for a local
HTTP service. The default server is https://portlight.616.pub, so pass --server
only for a separate deployment.

Prerequisites:
  export PORTLIGHT_TOKEN="<server token>"
  portlight update

Copy-paste prompt for another agent:
  Use portlight when you need a public HTTPS URL for a local HTTP service.

  If portlight is already installed, run portlight skill for the full agent guide.

  If portlight is missing, install it:
  macOS/Linux: curl -fsSL https://portlight.616.pub/install.sh | sh
  Windows PowerShell: irm https://portlight.616.pub/install.ps1 | iex

  Use PORTLIGHT_TOKEN if it is already set. If the user gives you a token, pass
  it with --token <token> or set PORTLIGHT_TOKEN for this shell.

  Start the local service first, then run:
  portlight expose --port <port> --ttl 30m --json

  If you only need to share files from a directory, run this inside that
  directory instead:
  portlight serve --ttl 30m --json

  Read the JSON ready event and use the url field. Keep the command running
  while the URL is needed. Stop the process, or wait for TTL, to close the URL.
  Do not print the token in logs.

Recommended agent flow:
  1. Start the local HTTP service first and wait for its health check, or use
     portlight serve when the task is only to share files from a directory.
  2. Start portlight expose or portlight serve in the background with --json.
  3. Read the JSON "ready" event and give the URL to the browser, webhook, or test.
  4. Stop portlight when the task is done. The tunnel closes when the CLI exits.

POSIX shell pattern:
  set -euo pipefail
  PORT=3000
  NAME="agent-${GITHUB_RUN_ID:-local}-$(date +%s)"

  portlight expose --port "$PORT" --name "$NAME" --ttl 30m --json > portlight.json &
  TUNNEL_PID=$!
  trap 'kill "$TUNNEL_PID" 2>/dev/null || true' EXIT

  for i in $(seq 1 80); do
    test -s portlight.json && break
    sleep 0.25
  done

  URL=$(python3 -c 'import json; print(json.load(open("portlight.json"))["url"])')
  echo "$URL"

Directory sharing:
  portlight serve --dir ./public --ttl 30m --json

  Directory listing is enabled. Hidden files and directories such as .git and
  .env are not served unless --include-hidden is set.

TTL:
  Tunnel lifetime is the expose process lifetime. Use --ttl to make the CLI exit
  automatically:

  portlight expose --port 3000 --name "$NAME" --ttl 30m --json

  You can still enforce an outer job limit with timeout, CI job limits, or a
  watchdog process.

  PowerShell agents can use --ttl, or Start-Process -PassThru and Stop-Process
  in a cleanup/finally block.

Naming:
  Omit --name for a random URL. Use --name only when a stable name helps logs or
  screenshots. Names are active only while the expose process is connected.

Failure handling:
  - name_in_use: choose a unique --name or omit it.
  - local service not ready: wait for localhost health before starting expose.
  - public URL is anonymous: do not expose private admin panels or secrets.
  - WebSocket/HMR is not supported yet; expose normal HTTP endpoints.
  - Do not print PORTLIGHT_TOKEN in logs.
`
