package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const LatestPath = "/releases/latest.json"

type Config struct {
	ServerURL      string
	CurrentVersion string
	GOOS           string
	GOARCH         string
	ExecutablePath string
	CheckOnly      bool
	Client         *http.Client
	Apply          func(downloaded, target string) error
}

type Result struct {
	CurrentVersion string
	LatestVersion  string
	URL            string
	Path           string
	AlreadyLatest  bool
	Updated        bool
	CheckOnly      bool
}

type Release struct {
	Version string        `json:"version"`
	Files   []ReleaseFile `json:"files"`
}

type ReleaseFile struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}

func Run(ctx context.Context, cfg Config) (Result, error) {
	if cfg.ServerURL == "" {
		return Result{}, errors.New("server URL required")
	}
	if cfg.CurrentVersion == "" {
		cfg.CurrentVersion = "dev"
	}
	if cfg.GOOS == "" {
		cfg.GOOS = runtime.GOOS
	}
	if cfg.GOARCH == "" {
		cfg.GOARCH = runtime.GOARCH
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	release, err := fetchLatest(ctx, cfg.Client, cfg.ServerURL)
	if err != nil {
		return Result{}, err
	}
	file, ok := release.fileFor(cfg.GOOS, cfg.GOARCH)
	if !ok {
		return Result{}, fmt.Errorf("no release file for %s/%s", cfg.GOOS, cfg.GOARCH)
	}
	downloadURL, err := resolveURL(cfg.ServerURL, file.URL)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		CurrentVersion: cfg.CurrentVersion,
		LatestVersion:  release.Version,
		URL:            downloadURL,
		CheckOnly:      cfg.CheckOnly,
	}
	if !isUpdateAvailable(cfg.CurrentVersion, release.Version) {
		result.AlreadyLatest = true
		return result, nil
	}
	if cfg.CheckOnly {
		return result, nil
	}
	if cfg.ExecutablePath == "" {
		cfg.ExecutablePath, err = os.Executable()
		if err != nil {
			return Result{}, err
		}
	}
	tmp, err := downloadAndVerify(ctx, cfg.Client, downloadURL, file.SHA256, cfg.ExecutablePath)
	if err != nil {
		return Result{}, err
	}
	if cfg.Apply == nil {
		cfg.Apply = replaceExecutable
	}
	if err := cfg.Apply(tmp, cfg.ExecutablePath); err != nil {
		_ = os.Remove(tmp)
		return Result{}, err
	}
	result.Path = cfg.ExecutablePath
	result.Updated = true
	return result, nil
}

func fetchLatest(ctx context.Context, client *http.Client, serverURL string) (Release, error) {
	latestURL, err := resolveURL(serverURL, LatestPath)
	if err != nil {
		return Release{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return Release{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("latest release request failed: %s", resp.Status)
	}
	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return Release{}, err
	}
	if release.Version == "" {
		return Release{}, errors.New("latest release missing version")
	}
	if len(release.Files) == 0 {
		return Release{}, errors.New("latest release missing files")
	}
	return release, nil
}

func (r Release) fileFor(goos, goarch string) (ReleaseFile, bool) {
	for _, file := range r.Files {
		if file.OS == goos && file.Arch == goarch {
			return file, true
		}
	}
	return ReleaseFile{}, false
}

func resolveURL(baseURL, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.IsAbs() {
		return u.String(), nil
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid server URL: %q", baseURL)
	}
	return base.ResolveReference(u).String(), nil
}

func isUpdateAvailable(current, latest string) bool {
	if current == "" || current == "dev" {
		return true
	}
	if current == latest {
		return false
	}
	cmp, ok := compareVersions(current, latest)
	if !ok {
		return current != latest
	}
	return cmp < 0
}

func compareVersions(a, b string) (int, bool) {
	left, ok := parseVersion(a)
	if !ok {
		return 0, false
	}
	right, ok := parseVersion(b)
	if !ok {
		return 0, false
	}
	for i := 0; i < len(left) || i < len(right); i++ {
		var l, r int
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		if l < r {
			return -1, true
		}
		if l > r {
			return 1, true
		}
	}
	return 0, true
}

func parseVersion(version string) ([]int, bool) {
	version = strings.TrimPrefix(version, "v")
	version = strings.Split(version, "-")[0]
	parts := strings.Split(version, ".")
	if len(parts) == 0 {
		return nil, false
	}
	numbers := make([]int, len(parts))
	for i, part := range parts {
		if part == "" {
			return nil, false
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		numbers[i] = n
	}
	return numbers, true
}

func downloadAndVerify(ctx context.Context, client *http.Client, rawURL, wantSHA, executablePath string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}
	dir := filepath.Dir(executablePath)
	base := filepath.Base(executablePath)
	tmp, err := os.CreateTemp(dir, "."+base+".update-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, hash), resp.Body)
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", closeErr
	}
	gotSHA := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(gotSHA, wantSHA) {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("checksum mismatch: got %s, want %s", gotSHA, wantSHA)
	}
	return tmpPath, nil
}

func replaceExecutable(downloaded, target string) error {
	if runtime.GOOS == "windows" {
		return scheduleWindowsReplace(downloaded, target)
	}
	info, err := os.Stat(target)
	if err == nil {
		_ = os.Chmod(downloaded, info.Mode().Perm())
	} else {
		_ = os.Chmod(downloaded, 0o755)
	}
	return os.Rename(downloaded, target)
}

func scheduleWindowsReplace(downloaded, target string) error {
	script := filepath.Join(filepath.Dir(target), fmt.Sprintf(".portlight-update-%d.cmd", os.Getpid()))
	content := fmt.Sprintf(`@echo off
setlocal
set "src=%s"
set "target=%s"
:retry
timeout /t 1 /nobreak >nul
move /y "%%src%%" "%%target%%" >nul
if errorlevel 1 goto retry
del "%%~f0"
`, downloaded, target)
	if err := os.WriteFile(script, []byte(content), 0o600); err != nil {
		return err
	}
	return exec.Command("cmd", "/C", "start", "", "/B", script).Start()
}
