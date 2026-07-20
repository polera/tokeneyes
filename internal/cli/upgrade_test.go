package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewerVersion(t *testing.T) {
	tests := []struct {
		candidate string
		current   string
		want      bool
	}{
		{candidate: "v1.10.0", current: "v1.9.9", want: true},
		{candidate: "v2.0.0", current: "1.99.99", want: true},
		{candidate: "v1.2.3", current: "v1.2.3", want: false},
		{candidate: "v1.2.3-beta.2", current: "v1.2.3-beta.1", want: true},
		{candidate: "v1.2.3", current: "v1.2.3-rc.1", want: true},
		{candidate: "not-a-version", current: "v1.0.0", want: false},
	}
	for _, test := range tests {
		t.Run(test.candidate+"_from_"+test.current, func(t *testing.T) {
			if got := newerVersion(test.candidate, test.current); got != test.want {
				t.Fatalf("newerVersion(%q, %q)=%t want %t", test.candidate, test.current, got, test.want)
			}
		})
	}
}

func TestGitHubUpdaterDownloadsVerifiesAndReplaces(t *testing.T) {
	binary := []byte("new tokeneyes binary")
	archive := tarGzipBinary(t, "tokeneyes", binary)
	digest := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  tokeneyes_linux_amd64.tar.gz\n", hex.EncodeToString(digest[:]))

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.2.0",
				"assets": []map[string]string{
					{"name": "tokeneyes_linux_amd64.tar.gz", "browser_download_url": server.URL + "/archive"},
					{"name": "checksums.txt", "browser_download_url": server.URL + "/checksums"},
				},
			})
		case "/archive":
			_, _ = w.Write(archive)
		case "/checksums":
			_, _ = w.Write([]byte(checksums))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	executable := filepath.Join(dir, "tokeneyes")
	if err := os.WriteFile(executable, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	updater := &githubUpdater{
		client:         server.Client(),
		apiURL:         server.URL + "/latest",
		statePath:      filepath.Join(dir, "cache", "update.json"),
		executablePath: func() (string, error) { return executable, nil },
		goos:           "linux",
		goarch:         "amd64",
		now:            func() time.Time { return now },
		checkInterval:  24 * time.Hour,
		deferDuration:  24 * time.Hour,
	}

	latest, upgraded, err := updater.Upgrade(context.Background(), "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if latest != "v1.2.0" || !upgraded {
		t.Fatalf("latest=%q upgraded=%t", latest, upgraded)
	}
	got, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("installed binary=%q want %q", got, binary)
	}
	state, err := updater.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.LatestVersion != "v1.2.0" || !state.LastChecked.Equal(now) {
		t.Fatalf("unexpected update state: %+v", state)
	}
}

func TestGitHubUpdaterCachesChecksAndHonorsDeferral(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.2.0", "assets": []any{}})
	}))
	defer server.Close()

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	updater := &githubUpdater{
		client:        server.Client(),
		apiURL:        server.URL,
		statePath:     filepath.Join(t.TempDir(), "update.json"),
		now:           func() time.Time { return now },
		checkInterval: 24 * time.Hour,
		deferDuration: 24 * time.Hour,
	}
	latest, available, err := updater.Check(context.Background(), "v1.1.0")
	if err != nil || latest != "v1.2.0" || !available || requests != 1 {
		t.Fatalf("latest=%q available=%t requests=%d err=%v", latest, available, requests, err)
	}
	if err := updater.Defer(latest); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	_, available, err = updater.Check(context.Background(), "v1.1.0")
	if err != nil || available || requests != 1 {
		t.Fatalf("deferred check: available=%t requests=%d err=%v", available, requests, err)
	}
	now = now.Add(24 * time.Hour)
	_, available, err = updater.Check(context.Background(), "v1.1.0")
	if err != nil || !available || requests != 2 {
		t.Fatalf("later check: available=%t requests=%d err=%v", available, requests, err)
	}
}

func TestGitHubUpdaterRejectsChecksumMismatch(t *testing.T) {
	archive := tarGzipBinary(t, "tokeneyes", []byte("untrusted binary"))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v2.0.0",
				"assets": []map[string]string{
					{"name": "tokeneyes_linux_amd64.tar.gz", "browser_download_url": server.URL + "/archive"},
					{"name": "checksums.txt", "browser_download_url": server.URL + "/checksums"},
				},
			})
		case "/archive":
			_, _ = w.Write(archive)
		case "/checksums":
			_, _ = fmt.Fprintln(w, strings.Repeat("0", 64), " tokeneyes_linux_amd64.tar.gz")
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	executable := filepath.Join(dir, "tokeneyes")
	if err := os.WriteFile(executable, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	updater := &githubUpdater{
		client:         server.Client(),
		apiURL:         server.URL + "/latest",
		statePath:      filepath.Join(dir, "update.json"),
		executablePath: func() (string, error) { return executable, nil },
		goos:           "linux",
		goarch:         "amd64",
		now:            time.Now,
	}
	_, _, err := updater.Upgrade(context.Background(), "v1.0.0")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error=%v", err)
	}
	got, readErr := os.ReadFile(executable)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old binary" {
		t.Fatalf("current executable was modified: %q", got)
	}
}

func TestOfferUpgradeCanBeDeferred(t *testing.T) {
	manager := &fakeUpgradeManager{latest: "v1.1.0", available: true}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	app := &App{
		Stdout:      stdout,
		Stderr:      stderr,
		Stdin:       strings.NewReader("not now\n"),
		Version:     "v1.0.0",
		Updater:     manager,
		Interactive: func() bool { return true },
	}
	app.offerUpgrade(context.Background())
	if manager.deferred != "v1.1.0" {
		t.Fatalf("deferred=%q", manager.deferred)
	}
	if !strings.Contains(stderr.String(), "Upgrade deferred for 24 hours") {
		t.Fatalf("unexpected prompt output: %s", stderr.String())
	}
}

func TestUpgradeCommand(t *testing.T) {
	manager := &fakeUpgradeManager{latest: "v1.1.0", upgraded: true}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	app := &App{Stdout: stdout, Stderr: stderr, Stdin: strings.NewReader(""), Version: "v1.0.0", Updater: manager}
	if code := app.Execute(context.Background(), []string{"upgrade"}); code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Upgraded tokeneyes from v1.0.0 to v1.1.0") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestVersionCommandUsesBuildVersion(t *testing.T) {
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	app := &App{Stdout: stdout, Stderr: stderr, Version: "v1.2.3"}
	if code := app.Execute(context.Background(), []string{"version"}); code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "tokeneyes v1.2.3 catalog=") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

type fakeUpgradeManager struct {
	latest    string
	available bool
	upgraded  bool
	deferred  string
}

func (m *fakeUpgradeManager) Check(context.Context, string) (string, bool, error) {
	return m.latest, m.available, nil
}

func (m *fakeUpgradeManager) Upgrade(context.Context, string) (string, bool, error) {
	return m.latest, m.upgraded, nil
}

func (m *fakeUpgradeManager) Defer(latest string) error {
	m.deferred = latest
	return nil
}

func tarGzipBinary(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	archive := tar.NewWriter(compressed)
	if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
