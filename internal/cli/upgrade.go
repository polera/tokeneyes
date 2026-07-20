package cli

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	latestReleaseURL = "https://api.github.com/repos/polera/tokeneyes/releases/latest"
	maxMetadataBytes = 2 << 20
	maxArchiveBytes  = 200 << 20
	maxBinaryBytes   = 150 << 20
)

type UpgradeManager interface {
	Check(context.Context, string) (latest string, available bool, err error)
	Upgrade(context.Context, string) (latest string, upgraded bool, err error)
	Defer(string) error
}

type githubUpdater struct {
	client         *http.Client
	apiURL         string
	statePath      string
	executablePath func() (string, error)
	goos           string
	goarch         string
	now            func() time.Time
	checkTimeout   time.Duration
	checkInterval  time.Duration
	deferDuration  time.Duration
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

type updateState struct {
	LastChecked   time.Time `json:"last_checked"`
	LatestVersion string    `json:"latest_version"`
	DeferredUntil time.Time `json:"deferred_until,omitempty"`
}

func NewGitHubUpdater() UpgradeManager {
	return &githubUpdater{
		client:         &http.Client{Timeout: 5 * time.Minute},
		apiURL:         latestReleaseURL,
		statePath:      defaultUpdateStatePath(),
		executablePath: os.Executable,
		goos:           runtime.GOOS,
		goarch:         runtime.GOARCH,
		now:            time.Now,
		checkTimeout:   3 * time.Second,
		checkInterval:  24 * time.Hour,
		deferDuration:  24 * time.Hour,
	}
}

func defaultUpdateStatePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "tokeneyes", "update.json")
}

func (u *githubUpdater) Check(ctx context.Context, current string) (string, bool, error) {
	if _, ok := parseSemver(current); !ok {
		return "", false, nil
	}
	state, _ := u.loadState()
	now := u.now()
	if state.DeferredUntil.After(now) {
		return state.LatestVersion, false, nil
	}
	if state.LatestVersion != "" && now.Sub(state.LastChecked) >= 0 && now.Sub(state.LastChecked) < u.checkInterval {
		return state.LatestVersion, newerVersion(state.LatestVersion, current), nil
	}
	if u.checkTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, u.checkTimeout)
		defer cancel()
	}
	release, err := u.latest(ctx)
	if err != nil {
		return "", false, err
	}
	state.LastChecked = now
	state.LatestVersion = release.TagName
	state.DeferredUntil = time.Time{}
	_ = u.saveState(state)
	return release.TagName, newerVersion(release.TagName, current), nil
}

func (u *githubUpdater) Upgrade(ctx context.Context, current string) (string, bool, error) {
	release, err := u.latest(ctx)
	if err != nil {
		return "", false, err
	}
	if _, stable := parseSemver(current); stable && !newerVersion(release.TagName, current) {
		_ = u.remember(release.TagName, time.Time{})
		return release.TagName, false, nil
	}
	archiveName := "tokeneyes_" + u.goos + "_" + u.goarch + ".tar.gz"
	binaryName := "tokeneyes"
	if u.goos == "windows" {
		archiveName = "tokeneyes_" + u.goos + "_" + u.goarch + ".zip"
		binaryName += ".exe"
	}
	archiveURL, ok := release.assetURL(archiveName)
	if !ok {
		return release.TagName, false, fmt.Errorf("release %s has no asset for %s/%s", release.TagName, u.goos, u.goarch)
	}
	checksumsURL, ok := release.assetURL("checksums.txt")
	if !ok {
		return release.TagName, false, fmt.Errorf("release %s has no checksums.txt", release.TagName)
	}
	checksums, err := u.download(ctx, checksumsURL, maxMetadataBytes)
	if err != nil {
		return release.TagName, false, fmt.Errorf("download checksums: %w", err)
	}
	expected, err := checksumFor(checksums, archiveName)
	if err != nil {
		return release.TagName, false, err
	}
	archive, err := u.download(ctx, archiveURL, maxArchiveBytes)
	if err != nil {
		return release.TagName, false, fmt.Errorf("download %s: %w", archiveName, err)
	}
	actual := sha256.Sum256(archive)
	if !bytes.Equal(actual[:], expected) {
		return release.TagName, false, fmt.Errorf("checksum mismatch for %s", archiveName)
	}
	binary, err := extractBinary(archiveName, binaryName, archive)
	if err != nil {
		return release.TagName, false, err
	}
	path, err := u.executablePath()
	if err != nil {
		return release.TagName, false, fmt.Errorf("locate current executable: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	if err := replaceExecutable(path, binary, u.goos); err != nil {
		return release.TagName, false, err
	}
	_ = u.remember(release.TagName, time.Time{})
	return release.TagName, true, nil
}

func (u *githubUpdater) Defer(latest string) error {
	return u.remember(latest, u.now().Add(u.deferDuration))
}

func (u *githubUpdater) remember(latest string, deferredUntil time.Time) error {
	return u.saveState(updateState{LastChecked: u.now(), LatestVersion: latest, DeferredUntil: deferredUntil})
}

func (u *githubUpdater) latest(ctx context.Context) (githubRelease, error) {
	var release githubRelease
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.apiURL, nil)
	if err != nil {
		return release, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "tokeneyes-updater")
	response, err := u.client.Do(req)
	if err != nil {
		return release, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return release, fmt.Errorf("GitHub latest-release request returned %s", response.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxMetadataBytes))
	if err := decoder.Decode(&release); err != nil {
		return release, fmt.Errorf("decode GitHub release: %w", err)
	}
	if _, ok := parseSemver(release.TagName); !ok {
		return release, fmt.Errorf("GitHub latest release has invalid version %q", release.TagName)
	}
	return release, nil
}

func (u *githubUpdater) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tokeneyes-updater")
	response, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("request returned %s", response.Status)
	}
	if response.ContentLength > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return data, nil
}

func (r githubRelease) assetURL(name string) (string, bool) {
	for _, asset := range r.Assets {
		if asset.Name == name && asset.URL != "" {
			return asset.URL, true
		}
	}
	return "", false
}

func (u *githubUpdater) loadState() (updateState, error) {
	var state updateState
	if u.statePath == "" {
		return state, nil
	}
	data, err := os.ReadFile(u.statePath) // #nosec G304 -- this is TokenEyes' fixed cache path.
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return updateState{}, err
	}
	return state, nil
}

func (u *githubUpdater) saveState(state updateState) error {
	if u.statePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(u.statePath), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(u.statePath), ".update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpName, u.statePath); err != nil && runtime.GOOS == "windows" {
		if removeErr := os.Remove(u.statePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return err
		}
		return os.Rename(tmpName, u.statePath)
	} else {
		return err
	}
}

func checksumFor(checksums []byte, name string) ([]byte, error) {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		value, err := hex.DecodeString(fields[0])
		if err != nil || len(value) != sha256.Size {
			return nil, fmt.Errorf("invalid checksum for %s", name)
		}
		return value, nil
	}
	return nil, fmt.Errorf("checksums.txt has no entry for %s", name)
}

func extractBinary(archiveName, binaryName string, data []byte) ([]byte, error) {
	if strings.HasSuffix(archiveName, ".zip") {
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", archiveName, err)
		}
		for _, file := range reader.File {
			if filepath.Base(file.Name) != binaryName || file.FileInfo().IsDir() {
				continue
			}
			stream, err := file.Open()
			if err != nil {
				return nil, err
			}
			binary, readErr := readBinary(stream)
			_ = stream.Close()
			return binary, readErr
		}
	} else {
		compressed, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", archiveName, err)
		}
		defer compressed.Close()
		reader := tar.NewReader(compressed)
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", archiveName, err)
			}
			if header.Typeflag == tar.TypeReg && filepath.Base(header.Name) == binaryName {
				return readBinary(reader)
			}
		}
	}
	return nil, fmt.Errorf("%s does not contain %s", archiveName, binaryName)
}

func readBinary(reader io.Reader) ([]byte, error) {
	binary, err := io.ReadAll(io.LimitReader(reader, maxBinaryBytes+1))
	if err != nil {
		return nil, err
	}
	if len(binary) == 0 {
		return nil, errors.New("release binary is empty")
	}
	if len(binary) > maxBinaryBytes {
		return nil, fmt.Errorf("release binary exceeds %d bytes", maxBinaryBytes)
	}
	return binary, nil
}

func replaceExecutable(path string, binary []byte, goos string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect current executable: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tokeneyes-upgrade-*")
	if err != nil {
		return fmt.Errorf("prepare upgrade next to %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	mode := info.Mode().Perm()
	if mode&0o111 == 0 {
		mode |= 0o755
	}
	if _, err = tmp.Write(binary); err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write upgraded executable: %w", err)
	}
	if goos != "windows" {
		if err := os.Rename(tmpName, path); err != nil {
			return fmt.Errorf("replace %s: %w", path, err)
		}
		return nil
	}
	backupFile, err := os.CreateTemp(dir, ".tokeneyes-previous-*.exe")
	if err != nil {
		return fmt.Errorf("prepare Windows upgrade: %w", err)
	}
	backup := backupFile.Name()
	_ = backupFile.Close()
	_ = os.Remove(backup)
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("move current executable for upgrade: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Rename(backup, path)
		return fmt.Errorf("install upgraded executable: %w", err)
	}
	_ = os.Remove(backup)
	return nil
}

type semVersion struct {
	major, minor, patch uint64
	prerelease          []string
}

func parseSemver(value string) (semVersion, bool) {
	var version semVersion
	value = strings.TrimSpace(strings.TrimPrefix(value, "v"))
	value, _, _ = strings.Cut(value, "+")
	core, prerelease, hasPrerelease := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version, false
	}
	numbers := []*uint64{&version.major, &version.minor, &version.patch}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semVersion{}, false
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semVersion{}, false
		}
		*numbers[i] = n
	}
	if hasPrerelease {
		if prerelease == "" {
			return semVersion{}, false
		}
		version.prerelease = strings.Split(prerelease, ".")
		for _, identifier := range version.prerelease {
			if identifier == "" {
				return semVersion{}, false
			}
		}
	}
	return version, true
}

func newerVersion(candidate, current string) bool {
	a, okA := parseSemver(candidate)
	b, okB := parseSemver(current)
	return okA && okB && compareSemver(a, b) > 0
}

func compareSemver(a, b semVersion) int {
	for _, pair := range [][2]uint64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(a.prerelease) == 0 && len(b.prerelease) == 0 {
		return 0
	}
	if len(a.prerelease) == 0 {
		return 1
	}
	if len(b.prerelease) == 0 {
		return -1
	}
	for i := 0; i < len(a.prerelease) && i < len(b.prerelease); i++ {
		if a.prerelease[i] == b.prerelease[i] {
			continue
		}
		an, aErr := strconv.ParseUint(a.prerelease[i], 10, 64)
		bn, bErr := strconv.ParseUint(b.prerelease[i], 10, 64)
		switch {
		case aErr == nil && bErr == nil:
			if an < bn {
				return -1
			}
			return 1
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		case a.prerelease[i] < b.prerelease[i]:
			return -1
		default:
			return 1
		}
	}
	if len(a.prerelease) < len(b.prerelease) {
		return -1
	}
	if len(a.prerelease) > len(b.prerelease) {
		return 1
	}
	return 0
}

func (a *App) installedVersion() string {
	if a.Version == "" {
		return "dev"
	}
	return a.Version
}

func (a *App) upgrade(ctx context.Context, args []string) int {
	if len(args) != 0 {
		a.error(errors.New("upgrade does not accept arguments"))
		return ExitUsage
	}
	if a.Updater == nil {
		a.error(errors.New("upgrades are unavailable in this build"))
		return ExitUsage
	}
	latest, upgraded, err := a.Updater.Upgrade(ctx, a.installedVersion())
	if err != nil {
		a.error(fmt.Errorf("upgrade failed: %w", err))
		return ExitUsage
	}
	if !upgraded {
		fmt.Fprintf(a.Stdout, "tokeneyes is up to date (%s).\n", latest)
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "Upgraded tokeneyes from %s to %s.\n", a.installedVersion(), latest)
	return ExitOK
}

func (a *App) offerUpgrade(ctx context.Context) {
	if a.Updater == nil || a.Interactive == nil || !a.Interactive() || os.Getenv("TOKENEYES_NO_UPDATE_CHECK") != "" {
		return
	}
	latest, available, err := a.Updater.Check(ctx, a.installedVersion())
	if err != nil || !available {
		return
	}
	fmt.Fprintf(a.Stderr, "\nA new TokenEyes release is available: %s → %s. Upgrade now? [y/N] ", a.installedVersion(), latest)
	answer, _ := bufio.NewReader(a.Stdin).ReadString('\n')
	if strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes") {
		upgradedTo, upgraded, upgradeErr := a.Updater.Upgrade(ctx, a.installedVersion())
		if upgradeErr != nil {
			a.error(fmt.Errorf("upgrade failed: %w", upgradeErr))
			return
		}
		if upgraded {
			fmt.Fprintf(a.Stderr, "Upgraded tokeneyes to %s.\n", upgradedTo)
		}
		return
	}
	if err := a.Updater.Defer(latest); err != nil {
		a.error(fmt.Errorf("could not defer upgrade reminder: %w", err))
		return
	}
	fmt.Fprintln(a.Stderr, "Upgrade deferred for 24 hours; run `tokeneyes upgrade` anytime.")
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
