package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ichuan/portlight/internal/client"
)

func runServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: portlight serve [options] [dir]")
		printFlagDefaults(stdout, fs)
	}
	serverURL := fs.String("server", defaultServerURL, "portlight server URL")
	token := fs.String("token", "", "server token; defaults to PORTLIGHT_TOKEN")
	dir := fs.String("dir", ".", "directory to serve")
	port := fs.Int("port", 0, "local HTTP port for the file server; 0 chooses a random port")
	name := fs.String("name", "", "optional requested public subdomain name")
	workers := fs.Int("workers", 8, "number of worker connections")
	ttl := fs.Duration("ttl", 0, "optional tunnel lifetime, for example 5m or 1h")
	includeHidden := fs.Bool("include-hidden", false, "serve hidden files and directories")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	quiet := fs.Bool("quiet", false, "suppress non-JSON ready output")
	if err := fs.Parse(args); err != nil {
		return helpExitCode(err)
	}
	if fs.NArg() > 1 {
		fmt.Fprintf(stderr, "unexpected serve argument %q\n", fs.Arg(1))
		return 2
	}
	dirSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "dir" {
			dirSet = true
		}
	})
	serveDir := *dir
	if fs.NArg() == 1 {
		if dirSet {
			fmt.Fprintln(stderr, "use either --dir or a positional dir, not both")
			return 2
		}
		serveDir = fs.Arg(0)
	}
	if *port < 0 || *port > 65535 {
		fmt.Fprintln(stderr, "port must be between 0 and 65535")
		return 2
	}
	if *ttl < 0 {
		fmt.Fprintln(stderr, "ttl must be positive")
		return 2
	}
	tokenValue := *token
	if tokenValue == "" {
		tokenValue = os.Getenv("PORTLIGHT_TOKEN")
	}
	root, err := filepath.Abs(serveDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	info, err := safeFileInfo(root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !info.IsDir() {
		fmt.Fprintf(stderr, "serve dir must be a directory: %s\n", serveDir)
		return 2
	}

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	localPort, err := listenerPort(listener)
	if err != nil {
		_ = listener.Close()
		fmt.Fprintln(stderr, err)
		return 1
	}
	fileServer := &http.Server{
		Handler:           http.FileServer(filteredFileSystem{root: root, includeHidden: *includeHidden}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		if err := fileServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = fileServer.Shutdown(ctx)
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *ttl > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *ttl)
		defer cancel()
	}

	becameReady := false
	err = client.Expose(ctx, client.Config{
		ServerURL: *serverURL,
		Token:     tokenValue,
		Name:      *name,
		LocalHost: "127.0.0.1",
		Port:      localPort,
		Workers:   *workers,
		OnReady: func(ready client.Ready) {
			becameReady = true
			if *jsonOut {
				_ = json.NewEncoder(stdout).Encode(ready)
				return
			}
			if !*quiet {
				fmt.Fprintf(stdout, "Serving %s as %s\n", displayServeDir(serveDir), ready.URL)
				fmt.Fprintln(stdout, "Press Ctrl+C to stop.")
			}
		},
	})
	select {
	case serverErr := <-serveErr:
		fmt.Fprintln(stderr, serverErr)
		return 1
	default:
	}
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	if errors.Is(err, context.DeadlineExceeded) && becameReady {
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

func listenerPort(listener net.Listener) (int, error) {
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address: %s", listener.Addr())
	}
	return addr.Port, nil
}

func displayServeDir(dir string) string {
	if dir == "" {
		return "."
	}
	return filepath.Clean(dir)
}

type filteredFileSystem struct {
	root          string
	includeHidden bool
}

func (fsys filteredFileSystem) Open(name string) (http.File, error) {
	target, err := fsys.resolve(name)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	return filteredFile{File: file, includeHidden: fsys.includeHidden}, nil
}

func (fsys filteredFileSystem) resolve(name string) (string, error) {
	clean := path.Clean("/" + strings.TrimPrefix(name, "/"))
	rel := strings.TrimPrefix(clean, "/")
	current := fsys.root
	if rel == "" {
		if _, err := safeFileInfo(current); err != nil {
			return "", err
		}
		return current, nil
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || strings.ContainsAny(part, `\/`) {
			return "", os.ErrNotExist
		}
		if !fsys.includeHidden && (isHiddenName(part) || platformUnsafeName(part)) {
			return "", os.ErrNotExist
		}
		current = filepath.Join(current, part)
		info, err := safeFileInfo(current)
		if err != nil {
			return "", err
		}
		if !fsys.includeHidden && isHiddenFileInfo(info) {
			return "", os.ErrNotExist
		}
	}
	return current, nil
}

type filteredFile struct {
	http.File
	includeHidden bool
}

func (file filteredFile) Readdir(count int) ([]os.FileInfo, error) {
	infos, err := file.File.Readdir(count)
	if file.includeHidden {
		return infos, err
	}
	filtered := make([]os.FileInfo, 0, len(infos))
	for _, info := range infos {
		if !isUnsafeFileInfo(info) && !isHiddenFileInfo(info) {
			filtered = append(filtered, info)
		}
	}
	return filtered, err
}

func isHiddenName(name string) bool {
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
}

func isHiddenFileInfo(info os.FileInfo) bool {
	return isHiddenName(info.Name()) || platformHidden(info)
}

func safeFileInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if isUnsafeFileInfo(info) {
		return nil, os.ErrNotExist
	}
	return info, nil
}

func isUnsafeFileInfo(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0 || platformUnsafeInfo(info)
}
