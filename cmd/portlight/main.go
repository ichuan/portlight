package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ichuan/portlight/internal/client"
	"github.com/ichuan/portlight/internal/server"
	"github.com/ichuan/portlight/internal/update"
)

var version = "dev"

const defaultServerURL = "https://portlight.616.pub"

var (
	updateGOOS       = runtime.GOOS
	updateGOARCH     = runtime.GOARCH
	updateExecutable = os.Executable
	updateApply      func(downloaded, target string) error
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
	case "server":
		return runServer(args[1:], stdout, stderr)
	case "expose":
		return runExpose(args[1:], stdout, stderr)
	case "update":
		return runUpdate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runServer(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: portlight server [options]")
		printFlagDefaults(stdout, fs)
	}
	listen := fs.String("listen", "127.0.0.1:8789", "HTTP listen address")
	publicBase := fs.String("public-base", "", "public HTTPS base URL, for example https://preview.example.com")
	token := fs.String("token", os.Getenv("PORTLIGHT_TOKEN"), "server token; defaults to PORTLIGHT_TOKEN")
	maxTunnels := fs.Int("max-tunnels", 64, "maximum active tunnels")
	maxWorkers := fs.Int("max-workers-per-tunnel", 8, "maximum idle workers per tunnel")
	requestTimeout := fs.Duration("request-timeout", 30*time.Second, "maximum time to wait for a worker")
	if err := fs.Parse(args); err != nil {
		return helpExitCode(err)
	}
	app, err := server.New(server.Config{
		PublicBase:          *publicBase,
		Token:               *token,
		MaxTunnels:          *maxTunnels,
		MaxWorkersPerTunnel: *maxWorkers,
		RequestTimeout:      *requestTimeout,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintf(stdout, "portlight server listening on %s\n", *listen)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
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
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	quiet := fs.Bool("quiet", false, "suppress non-JSON ready output")
	if err := fs.Parse(args); err != nil {
		return helpExitCode(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
				fmt.Fprintln(stdout, ready.URL)
			}
		},
	})
	if err == nil || errors.Is(err, context.Canceled) {
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
		fmt.Fprintf(stdout, "portlight %s is up to date\n", result.CurrentVersion)
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

func helpExitCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: portlight <command> [options]

Commands:
  server   run the public tunnel server
  expose   expose a local HTTP port
  update   update this portlight binary
  version  print version

Examples:
  portlight server --listen 127.0.0.1:8789 --public-base https://preview.example.com
  portlight expose --port 3000 --json
  portlight update`)
}

func printFlagDefaults(w io.Writer, fs *flag.FlagSet) {
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	fs.SetOutput(w)
	text := strings.ReplaceAll(buf.String(), "  -", "  --")
	fmt.Fprint(w, text)
}
