// Package provision installs and discovers Wok's private Chromium headless shell.
package provision

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const metadataURL = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$`)

type manifest struct {
	Version    string `json:"version"`
	Platform   string `json:"platform"`
	Executable string `json:"executable"`
}
type config struct {
	root, platform, metadata     string
	client                       *http.Client
	validateDownload             func(string, string, string) error
	downloadLimit, expandedLimit int64
	entryLimit                   int
}

func defaults() (config, error) {
	platform, err := platformFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return config{}, err
	}
	root := os.Getenv("WOK_BROWSER_CACHE")
	if root == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return config{}, fmt.Errorf("Wok browser cache: %w", err)
		}
		root = filepath.Join(base, "wok", "browsers")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return config{}, err
	}
	return config{root: root, platform: platform, metadata: metadataURL, client: &http.Client{Timeout: 10 * time.Minute}, validateDownload: validateDownload, downloadLimit: 256 << 20, expandedLimit: 1 << 30, entryLimit: 10000}, nil
}

// executableName is the headless shell binary inside a Chrome for Testing archive.
func executableName(platform string) string {
	if strings.HasPrefix(platform, "win") {
		return "chrome-headless-shell.exe"
	}
	return "chrome-headless-shell"
}
func platformFor(goos, arch string) (string, error) {
	switch goos + "/" + arch {
	case "linux/amd64":
		return "linux64", nil
	case "darwin/amd64":
		return "mac-x64", nil
	case "darwin/arm64":
		return "mac-arm64", nil
	case "windows/amd64":
		return "win64", nil
	}
	return "", fmt.Errorf("Wok cannot install Chromium headless shell on %s/%s; install a compatible Chromium browser and set WOK_BROWSER", goos, arch)
}

// Cached finds a validated cached executable without making network requests.
func Cached() (string, error) {
	c, err := defaults()
	if err != nil {
		return "", err
	}
	return cached(c)
}

// Ensure returns the cached executable or installs official Stable Chromium.
func Ensure(ctx context.Context) (string, error) {
	c, err := defaults()
	if err != nil {
		return "", err
	}
	return ensure(ctx, c)
}

func cached(c config) (string, error) {
	filename := filepath.Join(c.root, "current.json")
	if err := regularFile(filename, false); err != nil {
		return "", fmt.Errorf("Wok browser cache manifest: %w", err)
	}
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil {
		return "", err
	}
	if len(data) > 8192 {
		return "", errors.New("Wok browser cache manifest exceeds size limit")
	}
	var m manifest
	if err = json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("Wok browser cache manifest: %w", err)
	}
	if !versionPattern.MatchString(m.Version) || m.Platform != c.platform {
		return "", errors.New("Wok browser cache has an incompatible manifest")
	}
	if err = validRelative(m.Executable); err != nil {
		return "", err
	}
	parts := strings.Split(m.Executable, "/")
	if len(parts) != 3 || parts[0] != m.Version || !strings.HasPrefix(parts[1], c.platform+"-") || parts[2] != executableName(c.platform) {
		return "", errors.New("Wok browser cache has an invalid executable path")
	}
	current := c.root
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("Wok browser cache: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("Wok browser cache contains a symbolic link")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", errors.New("Wok browser cache parent is not a directory")
		}
	}
	if err = regularFile(current, true); err != nil {
		return "", fmt.Errorf("Wok browser cache executable: %w", err)
	}
	return current, nil
}
func regularFile(filename string, executable bool) error {
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("expected a regular file")
	}
	if executable && info.Size() == 0 {
		return errors.New("executable is empty")
	}
	// Windows has no execute permission bit; the .exe name marks executables.
	if executable && runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return errors.New("file is not executable")
	}
	return nil
}
func validRelative(name string) error {
	if name == "" || strings.Contains(name, "\\") || strings.Contains(name, ":") || strings.HasPrefix(name, "/") || path.Clean(name) != strings.TrimSuffix(name, "/") {
		return fmt.Errorf("unsafe browser archive or cache path %q", name)
	}
	for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if part == ".." || part == "." || part == "" {
			return fmt.Errorf("unsafe browser archive or cache path %q", name)
		}
	}
	return nil
}
func validateDownload(version, platform, raw string) error {
	expected := "https://storage.googleapis.com/chrome-for-testing-public/" + version + "/" + platform + "/chrome-headless-shell-" + platform + ".zip"
	if raw != expected {
		return errors.New("Chromium metadata contains an unexpected download URL")
	}
	return nil
}
func ensure(ctx context.Context, c config) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if executable, err := cached(c); err == nil {
		return executable, nil
	}
	if err := os.MkdirAll(c.root, 0700); err != nil {
		return "", fmt.Errorf("create Wok browser cache: %w", err)
	}
	unlock, err := lock(ctx, filepath.Join(c.root, ".install.lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	if executable, err := cached(c); err == nil {
		return executable, nil
	}
	version, download, err := resolve(ctx, c)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(c.root, ".download-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	archive := filepath.Join(stage, "browser.zip")
	if err = fetchArchive(ctx, c, download, archive); err != nil {
		return "", err
	}
	payload := filepath.Join(stage, "payload")
	if err = extract(archive, payload, c); err != nil {
		return "", fmt.Errorf("extract Chromium: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	parent := filepath.Join(c.root, version)
	if err = os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Wok browser version cache is not a real directory")
	}
	// A unique destination permits recovery without replacing existing user data.
	name := c.platform + "-" + strings.TrimPrefix(filepath.Base(stage), ".download-")
	destination := filepath.Join(parent, name)
	if err = os.Rename(payload, destination); err != nil {
		return "", fmt.Errorf("publish Wok browser: %w", err)
	}
	m := manifest{Version: version, Platform: c.platform, Executable: path.Join(version, name, executableName(c.platform))}
	pointer, err := os.CreateTemp(c.root, ".current-")
	if err != nil {
		return "", err
	}
	pointerName := pointer.Name()
	defer os.Remove(pointerName)
	err = json.NewEncoder(pointer).Encode(m)
	if err == nil {
		err = pointer.Sync()
	}
	closeErr := pointer.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err = os.Rename(pointerName, filepath.Join(c.root, "current.json")); err != nil {
		return "", err
	}
	return cached(c)
}
func resolve(ctx context.Context, c config) (string, string, error) {
	response, err := get(ctx, c, c.metadata)
	if err != nil {
		return "", "", fmt.Errorf("fetch Chromium metadata (check network access): %w", err)
	}
	defer response.Body.Close()
	var metadata struct {
		Channels map[string]struct {
			Version   string `json:"version"`
			Downloads map[string][]struct {
				Platform string `json:"platform"`
				URL      string `json:"url"`
			} `json:"downloads"`
		} `json:"channels"`
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return "", "", err
	}
	if len(data) > 4<<20 {
		return "", "", errors.New("Chromium metadata exceeds size limit")
	}
	if err = json.Unmarshal(data, &metadata); err != nil {
		return "", "", fmt.Errorf("decode Chromium metadata: %w", err)
	}
	stable, ok := metadata.Channels["Stable"]
	if !ok || !versionPattern.MatchString(stable.Version) {
		return "", "", errors.New("Chromium metadata has no valid Stable version")
	}
	for _, download := range stable.Downloads["chrome-headless-shell"] {
		if download.Platform == c.platform {
			if err = c.validateDownload(stable.Version, c.platform, download.URL); err != nil {
				return "", "", err
			}
			return stable.Version, download.URL, nil
		}
	}
	return "", "", fmt.Errorf("Chromium Stable has no headless shell for %s", c.platform)
}
func get(ctx context.Context, c config, raw string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	// Redirects must not bypass the official archive origin.
	client := *c.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return errors.New("unexpected browser download redirect")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("HTTP %d from %s", response.StatusCode, request.URL.Host)
	}
	return response, nil
}
func fetchArchive(ctx context.Context, c config, raw, filename string) error {
	if _, err := url.Parse(raw); err != nil {
		return err
	}
	response, err := get(ctx, c, raw)
	if err != nil {
		return fmt.Errorf("download Chromium (check network access): %w", err)
	}
	defer response.Body.Close()
	if response.ContentLength > c.downloadLimit {
		return errors.New("Chromium archive exceeds download size limit")
	}
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(response.Body, c.downloadLimit+1))
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("download Chromium: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if n > c.downloadLimit {
		return errors.New("Chromium archive exceeds download size limit")
	}
	return nil
}
func extract(archive, destination string, c config) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) > c.entryLimit {
		return errors.New("Chromium archive exceeds entry limit")
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return err
	}
	prefix := "chrome-headless-shell-" + c.platform + "/"
	var total int64
	for _, entry := range reader.File {
		if err = validRelative(entry.Name); err != nil {
			return err
		}
		if !strings.HasPrefix(entry.Name, prefix) {
			return errors.New("unexpected Chromium archive root")
		}
		if entry.Mode()&os.ModeSymlink != 0 || (!entry.Mode().IsRegular() && !entry.FileInfo().IsDir()) {
			return errors.New("Chromium archive contains a link or special file")
		}
		relative := strings.TrimPrefix(entry.Name, prefix)
		if relative == "" {
			continue
		}
		target := filepath.Join(destination, filepath.FromSlash(relative))
		if entry.FileInfo().IsDir() {
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if entry.UncompressedSize64 > uint64(c.expandedLimit-total) {
			return errors.New("Chromium archive exceeds expanded size limit")
		}
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if entry.Mode().Perm()&0111 != 0 || relative == executableName(c.platform) {
			mode = 0755
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		source, err := entry.Open()
		if err != nil {
			out.Close()
			return err
		}
		n, copyErr := io.Copy(out, io.LimitReader(source, c.expandedLimit-total+1))
		sourceErr := source.Close()
		outErr := out.Close()
		total += n
		if copyErr != nil {
			return copyErr
		}
		if sourceErr != nil {
			return sourceErr
		}
		if outErr != nil {
			return outErr
		}
		if total > c.expandedLimit {
			return errors.New("Chromium archive exceeds expanded size limit")
		}
	}
	return regularFile(filepath.Join(destination, executableName(c.platform)), true)
}
