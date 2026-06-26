package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ichuan/portlight/internal/server"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "--version", "version":
			fmt.Fprintf(stdout, "portlight-server %s\n", version)
			return 0
		case "-h", "--help", "help":
			printUsage(stdout)
			return 0
		}
	}

	fs := flag.NewFlagSet("portlight-server", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() { printUsage(stdout) }
	listen := fs.String("listen", "127.0.0.1:8789", "HTTP listen address")
	publicBase := fs.String("public-base", "", "public HTTPS base URL, for example https://preview.example.com")
	token := fs.String("token", "", "server token; defaults to PORTLIGHT_TOKEN")
	maxTunnels := fs.Int("max-tunnels", 64, "maximum active tunnels")
	maxWorkers := fs.Int("max-workers-per-tunnel", 8, "maximum idle workers per tunnel")
	requestTimeout := fs.Duration("request-timeout", 30*time.Second, "maximum time to wait for a worker")
	if err := fs.Parse(args); err != nil {
		return helpExitCode(err)
	}
	tokenValue := *token
	if tokenValue == "" {
		tokenValue = os.Getenv("PORTLIGHT_TOKEN")
	}
	app, err := server.New(server.Config{
		PublicBase:          *publicBase,
		Token:               tokenValue,
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
	fmt.Fprintf(stdout, "portlight-server listening on %s\n", *listen)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: portlight-server [options]")
	fs := flag.NewFlagSet("portlight-server", flag.ContinueOnError)
	fs.String("listen", "127.0.0.1:8789", "HTTP listen address")
	fs.String("public-base", "", "public HTTPS base URL, for example https://preview.example.com")
	fs.String("token", "", "server token; defaults to PORTLIGHT_TOKEN")
	fs.Int("max-tunnels", 64, "maximum active tunnels")
	fs.Int("max-workers-per-tunnel", 8, "maximum idle workers per tunnel")
	fs.Duration("request-timeout", 30*time.Second, "maximum time to wait for a worker")
	printFlagDefaults(w, fs)
}

func helpExitCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func printFlagDefaults(w io.Writer, fs *flag.FlagSet) {
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	fs.SetOutput(w)
	text := strings.ReplaceAll(buf.String(), "  -", "  --")
	fmt.Fprint(w, text)
}
