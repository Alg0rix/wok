package provision

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type zipEntry struct {
	name, body string
	mode       os.FileMode
}

func archiveBytes(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		f, err := w.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func fixture(t *testing.T, archive []byte) (config, *atomic.Int32) {
	t.Helper()
	var downloads atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			_ = json.NewEncoder(w).Encode(map[string]any{"channels": map[string]any{"Stable": map[string]any{"version": "123.0.1.2", "downloads": map[string]any{"chrome-headless-shell": []map[string]string{{"platform": "linux64", "url": server.URL + "/archive"}}}}}})
			return
		}
		if r.URL.Path == "/archive" {
			downloads.Add(1)
			_, _ = w.Write(archive)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return config{root: t.TempDir(), platform: "linux64", metadata: server.URL + "/metadata", client: server.Client(), validateDownload: func(version, platform, raw string) error {
		if raw != server.URL+"/archive" {
			return errors.New("unexpected test URL")
		}
		return nil
	}, downloadLimit: 1 << 20, expandedLimit: 1 << 20, entryLimit: 100}, &downloads
}
func goodArchive(t *testing.T) []byte {
	return archiveBytes(t, zipEntry{"chrome-headless-shell-linux64/chrome-headless-shell", "binary", 0600}, zipEntry{"chrome-headless-shell-linux64/resources/data", "resource", 0600})
}
func TestInstallCacheReuseAndNoNetwork(t *testing.T) {
	c, downloads := fixture(t, goodArchive(t))
	executable, err := ensure(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(executable); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0755) {
		t.Fatalf("executable mode: %v %v", info, err)
	}
	c.metadata = "http://127.0.0.1:1/offline"
	cachedPath, err := ensure(context.Background(), c)
	if err != nil || cachedPath != executable {
		t.Fatalf("cache reuse: %q %v", cachedPath, err)
	}
	if downloads.Load() != 1 {
		t.Fatal("redownloaded cached browser")
	}
	t.Setenv("WOK_BROWSER_CACHE", c.root)
	// Public Cached checks this machine's platform; fixtures are linux64.
	if platform, err := platformFor("linux", "amd64"); err != nil || platform != "linux64" {
		t.Fatal(platform, err)
	}
}
func TestConcurrentEnsureInstallsOnce(t *testing.T) {
	c, downloads := fixture(t, goodArchive(t))
	var wg sync.WaitGroup
	results := make(chan string, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			executable, err := ensure(context.Background(), c)
			if err != nil {
				t.Error(err)
			}
			results <- executable
		}()
	}
	wg.Wait()
	close(results)
	first := ""
	for executable := range results {
		if first == "" {
			first = executable
		}
		if executable != first {
			t.Fatal("different cache paths")
		}
	}
	if downloads.Load() != 1 {
		t.Fatalf("downloaded %d times", downloads.Load())
	}
}
func TestInvalidCacheRecovers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no execute permission bit to invalidate")
	}
	c, downloads := fixture(t, goodArchive(t))
	executable, err := ensure(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(executable, 0600); err != nil {
		t.Fatal(err)
	}
	replacement, err := ensure(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if replacement == executable || downloads.Load() != 2 {
		t.Fatal("did not repair stale cache")
	}
	if _, err = os.Stat(executable); err != nil {
		t.Fatal("deleted previous cache data")
	}
}
func TestCachedRejectsUnsafeManifests(t *testing.T) {
	for _, name := range []string{"../outside", "/tmp/browser", `123.0.1.2\linux64-x\chrome-headless-shell`, "123.0.1.2/linux64-x/../chrome-headless-shell"} {
		t.Run(name, func(t *testing.T) {
			c, _ := fixture(t, goodArchive(t))
			data, _ := json.Marshal(manifest{"123.0.1.2", "linux64", name})
			if err := os.WriteFile(filepath.Join(c.root, "current.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := cached(c); err == nil {
				t.Fatal("accepted unsafe manifest")
			}
		})
	}
	c, _ := fixture(t, goodArchive(t))
	executable, err := ensure(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("/bin/sh", executable); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symbolic links need Windows developer mode")
		}
		t.Fatal(err)
	}
	if _, err = cached(c); err == nil {
		t.Fatal("accepted executable symlink")
	}
}
func TestHTTPAndCancellation(t *testing.T) {
	c, _ := fixture(t, goodArchive(t))
	c.metadata = strings.TrimSuffix(c.metadata, "/metadata") + "/missing"
	if _, err := ensure(context.Background(), c); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("got %v", err)
	}
	c, _ = fixture(t, goodArchive(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ensure(ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	// A blocked metadata request observes its caller's cancellation.
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer server.Close()
	c.metadata = server.URL
	c.client = server.Client()
	ctx, cancel = context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := ensure(ctx, c); result <- err }()
	<-entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
func TestArchiveValidationAndLimits(t *testing.T) {
	cases := []struct {
		name    string
		entries []zipEntry
		adjust  func(*config)
	}{
		{"traversal", []zipEntry{{"chrome-headless-shell-linux64/../outside", "x", 0600}}, nil},
		{"absolute", []zipEntry{{"/outside", "x", 0600}}, nil},
		{"backslash", []zipEntry{{`chrome-headless-shell-linux64\outside`, "x", 0600}}, nil},
		{"symlink", []zipEntry{{"chrome-headless-shell-linux64/chrome-headless-shell", "/bin/sh", os.ModeSymlink | 0777}}, nil},
		{"wrong root", []zipEntry{{"wrong/chrome-headless-shell", "x", 0755}}, nil},
		{"download cap", nil, func(c *config) { c.downloadLimit = 1 }},
		{"expanded cap", nil, func(c *config) { c.expandedLimit = 1 }},
		{"entry cap", nil, func(c *config) { c.entryLimit = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := goodArchive(t)
			if tc.entries != nil {
				archive = archiveBytes(t, tc.entries...)
			}
			c, _ := fixture(t, archive)
			if tc.adjust != nil {
				tc.adjust(&c)
			}
			if _, err := ensure(context.Background(), c); err == nil {
				t.Fatal("accepted invalid archive")
			}
			if _, err := os.Stat(filepath.Join(c.root, "current.json")); !os.IsNotExist(err) {
				t.Fatal("published invalid install")
			}
			entries, _ := os.ReadDir(c.root)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".download-") {
					t.Fatal("left staging directory")
				}
			}
		})
	}
}
func TestLockContextBounded(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "lock")
	unlock, err := lock(context.Background(), filename)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err = lock(ctx, filename); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}
func TestOfficialDownloadValidation(t *testing.T) {
	good := "https://storage.googleapis.com/chrome-for-testing-public/123.0.1.2/linux64/chrome-headless-shell-linux64.zip"
	if err := validateDownload("123.0.1.2", "linux64", good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(good, "https:", "http:", 1), good + "?extra=1", strings.Replace(good, "storage.googleapis.com", "evil.example", 1)} {
		if err := validateDownload("123.0.1.2", "linux64", bad); err == nil {
			t.Fatal("accepted unofficial download")
		}
	}
	for _, platform := range [][2]string{{"linux", "arm64"}, {"windows", "arm64"}} {
		if _, err := platformFor(platform[0], platform[1]); err == nil {
			t.Fatal("accepted unsupported platform")
		}
	}
	if platform, err := platformFor("windows", "amd64"); err != nil || platform != "win64" || executableName(platform) != "chrome-headless-shell.exe" {
		t.Fatal(platform, err)
	}
}

func TestArchiveCRCVerifiedBeforePublication(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.CreateHeader(&zip.FileHeader{Name: "chrome-headless-shell-linux64/chrome-headless-shell", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("unique-binary-payload"))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	offset := bytes.Index(data, []byte("unique-binary-payload"))
	if offset < 0 {
		t.Fatal("fixture payload missing")
	}
	data[offset] = 'X'
	c, _ := fixture(t, data)
	if _, err = ensure(context.Background(), c); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum rejection, got %v", err)
	}
	if _, err = os.Stat(filepath.Join(c.root, "current.json")); !os.IsNotExist(err) {
		t.Fatal("published archive with invalid checksum")
	}
}
