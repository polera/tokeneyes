package tokeneyes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	DefaultMaxFileBytes  = int64(5 << 20)
	DefaultMaxTotalBytes = int64(100 << 20)
	DefaultMaxMediaBytes = int64(50 << 20)
	DefaultMaxMediaCount = 100
	DefaultMaxPages      = 600
	DefaultMaxDuration   = 4 * 60 * 60
)

type FileCollector struct{}

func NewFileCollector() *FileCollector { return &FileCollector{} }

type ignoreRule struct {
	re     *regexp.Regexp
	negate bool
}

type collectionEvent struct {
	candidate  *fileCandidate
	warning    string
	incomplete bool
}

type fileCandidate struct {
	sequence   int
	eventIndex int
	abs        string
	rel        string
	size       int64
}

type preparedFile struct {
	candidate  fileCandidate
	content    []byte
	sha        string
	asset      Asset
	extracted  []byte
	media      bool
	inspectErr error
	readErr    error
}

func (FileCollector) Collect(ctx context.Context, req CollectRequest) (Collection, error) {
	if req.Root == "" {
		req.Root = "."
	}
	root, err := filepath.Abs(req.Root)
	if err != nil {
		return Collection{}, err
	}
	if req.MaxFileBytes <= 0 {
		req.MaxFileBytes = DefaultMaxFileBytes
	}
	if req.MaxTotalBytes <= 0 {
		req.MaxTotalBytes = DefaultMaxTotalBytes
	}
	if req.MaxMediaBytes <= 0 {
		req.MaxMediaBytes = DefaultMaxMediaBytes
	}
	if req.MaxMediaCount <= 0 {
		req.MaxMediaCount = DefaultMaxMediaCount
	}
	if req.MaxPages <= 0 {
		req.MaxPages = DefaultMaxPages
	}
	if req.MaxDuration <= 0 {
		req.MaxDuration = DefaultMaxDuration * time.Second
	}

	rules := loadIgnoreRules(root)
	paths, err := requestedPaths(ctx, root, req)
	if err != nil {
		return Collection{}, err
	}
	var out Collection
	seen := make(map[string]bool)

	addBytes := func(label, path, kind string, b []byte) {
		b = normalizeText(b)
		h := sha256.Sum256(b)
		out.Sources = append(out.Sources, Source{Label: label, Path: path, Kind: kind, SHA256: hex.EncodeToString(h[:]), Bytes: int64(len(b)), Content: b})
		out.TotalBytes += int64(len(b))
	}

	if req.Prompt != "" {
		addBytes("prompt", "", "prompt", []byte(req.Prompt))
	}
	if req.PromptFile != "" {
		p := req.PromptFile
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		// #nosec G304 -- reading a caller-named prompt file is the documented purpose of --prompt-file.
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			return Collection{}, fmt.Errorf("read prompt file: %w", readErr)
		}
		if int64(len(b)) > req.MaxFileBytes {
			return Collection{}, fmt.Errorf("prompt file exceeds --max-file-bytes")
		}
		if out.TotalBytes+int64(len(b)) > req.MaxTotalBytes {
			return Collection{}, fmt.Errorf("prompt file exceeds --max-total-bytes")
		}
		addBytes("prompt-file:"+displayPath(root, p), displayPath(root, p), "prompt-file", b)
	}
	if req.ReadStdin {
		if req.Stdin == nil {
			req.Stdin = os.Stdin
		}
		b, readErr := io.ReadAll(io.LimitReader(req.Stdin, req.MaxTotalBytes-out.TotalBytes+1))
		if readErr != nil {
			return Collection{}, fmt.Errorf("read stdin: %w", readErr)
		}
		if out.TotalBytes+int64(len(b)) > req.MaxTotalBytes {
			return Collection{}, fmt.Errorf("stdin exceeds --max-total-bytes")
		}
		addBytes("stdin", "", "stdin", b)
	}

	events, err := discoverCollectionEvents(ctx, root, paths, req, rules, seen)
	if err != nil {
		return Collection{}, err
	}
	if err := collectEvents(ctx, events, req, &out); err != nil {
		return Collection{}, err
	}
	if err := attachTranscripts(root, req, &out); err != nil {
		return Collection{}, err
	}

	sort.Slice(out.Sources, func(i, j int) bool {
		if out.Sources[i].Kind != out.Sources[j].Kind {
			return out.Sources[i].Kind < out.Sources[j].Kind
		}
		return out.Sources[i].Label < out.Sources[j].Label
	})
	return out, nil
}

func discoverCollectionEvents(ctx context.Context, root string, paths []string, req CollectRequest, rules []ignoreRule, seen map[string]bool) ([]collectionEvent, error) {
	var events []collectionEvent
	addWarning := func(warning string, incomplete bool) {
		events = append(events, collectionEvent{warning: warning, incomplete: incomplete})
	}
	addCandidate := func(abs string, info os.FileInfo) {
		if seen[abs] {
			return
		}
		seen[abs] = true
		if !info.Mode().IsRegular() {
			return
		}
		candidate := fileCandidate{eventIndex: len(events), abs: abs, rel: displayPath(root, abs), size: info.Size()}
		events = append(events, collectionEvent{candidate: &candidate})
	}

	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		abs, absErr := filepath.Abs(p)
		if absErr != nil {
			addWarning(fmt.Sprintf("skip %s: %v", p, absErr), true)
			continue
		}
		info, statErr := os.Lstat(abs)
		if statErr != nil {
			addWarning(fmt.Sprintf("skip %s: %v", p, statErr), true)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			addWarning("skip symlink: "+displayPath(root, abs), false)
			continue
		}
		if info.IsDir() {
			walkErr := filepath.WalkDir(abs, func(candidate string, entry os.DirEntry, walkErr error) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if walkErr != nil {
					addWarning(fmt.Sprintf("skip %s: %v", displayPath(root, candidate), walkErr), true)
					return nil
				}
				rel := displayPath(root, candidate)
				if candidate == abs {
					return nil
				}
				if entry.Type()&os.ModeSymlink != 0 {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if entry.IsDir() {
					if shouldSkipDir(rel, req.IncludeHidden) || ignored(rel+"/", rules) {
						return filepath.SkipDir
					}
					return nil
				}
				if ignored(rel, rules) {
					return nil
				}
				entryInfo, err := entry.Info()
				if err != nil {
					if !seen[candidate] {
						seen[candidate] = true
						addWarning(fmt.Sprintf("skip %s: %v", rel, err), true)
					}
					return nil
				}
				addCandidate(candidate, entryInfo)
				return nil
			})
			if walkErr != nil {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				addWarning(walkErr.Error(), true)
			}
			continue
		}
		if ignored(displayPath(root, abs), rules) {
			continue
		}
		addCandidate(abs, info)
	}
	return events, nil
}

func collectEvents(ctx context.Context, events []collectionEvent, req CollectRequest, out *Collection) error {
	var candidates []fileCandidate
	for _, event := range events {
		if event.candidate != nil {
			candidate := *event.candidate
			candidate.sequence = len(candidates)
			candidates = append(candidates, candidate)
		}
	}
	eventIndex := 0
	commit := func(result preparedFile) {
		for eventIndex < result.candidate.eventIndex {
			commitCollectionNotice(events[eventIndex], out)
			eventIndex++
		}
		commitPreparedFile(result, req, out)
		eventIndex++
	}
	if err := processCandidates(ctx, candidates, boundedWorkers(req.Workers), req, out, prepareFile, commit); err != nil {
		return err
	}
	for eventIndex < len(events) {
		commitCollectionNotice(events[eventIndex], out)
		eventIndex++
	}
	return nil
}

func commitCollectionNotice(event collectionEvent, out *Collection) {
	if event.warning != "" {
		out.Warnings = append(out.Warnings, event.warning)
	}
	if event.incomplete {
		out.Incomplete = true
	}
}

func processCandidates(ctx context.Context, candidates []fileCandidate, workers int, req CollectRequest, out *Collection, prepare func(fileCandidate) preparedFile, commit func(preparedFile)) error {
	if len(candidates) == 0 {
		return ctx.Err()
	}
	if workers > len(candidates) {
		workers = len(candidates)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan fileCandidate, workers)
	results := make(chan preparedFile, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case candidate, ok := <-jobs:
					if !ok {
						return
					}
					result := prepare(candidate)
					select {
					case results <- result:
					case <-workerCtx.Done():
						return
					}
				}
			}
		}()
	}
	stop := func() {
		cancel()
		close(jobs)
		wg.Wait()
	}

	readLimit := req.MaxFileBytes
	if req.MaxMediaBytes > readLimit {
		readLimit = req.MaxMediaBytes
	}
	nextDispatch, nextCommit := 0, 0
	pending := make(map[int]preparedFile, workers)
	for nextCommit < len(candidates) {
		for nextDispatch < len(candidates) && nextDispatch < nextCommit+workers {
			candidate := candidates[nextDispatch]
			if candidate.size > readLimit || out.TotalBytes+candidate.size > req.MaxTotalBytes {
				pending[nextDispatch] = preparedFile{candidate: candidate}
				nextDispatch++
				continue
			}
			select {
			case jobs <- candidate:
				nextDispatch++
			case <-ctx.Done():
				stop()
				return ctx.Err()
			}
		}
		if result, ok := pending[nextCommit]; ok {
			commit(result)
			delete(pending, nextCommit)
			nextCommit++
			continue
		}
		select {
		case result := <-results:
			pending[result.candidate.sequence] = result
		case <-ctx.Done():
			stop()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	return nil
}

func prepareFile(candidate fileCandidate) preparedFile {
	result := preparedFile{candidate: candidate}
	// #nosec G304 -- candidate.abs is a collection target the caller asked to be counted.
	b, err := os.ReadFile(candidate.abs)
	if err != nil {
		result.readErr = err
		return result
	}
	result.content = b
	h := sha256.Sum256(b)
	result.sha = hex.EncodeToString(h[:])
	result.asset, result.extracted, result.media, result.inspectErr = inspectMedia(candidate.rel, candidate.rel, result.sha, b)
	return result
}

func commitPreparedFile(result preparedFile, req CollectRequest, out *Collection) {
	candidate := result.candidate
	readLimit := req.MaxFileBytes
	if req.MaxMediaBytes > readLimit {
		readLimit = req.MaxMediaBytes
	}
	if candidate.size > readLimit {
		out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: file is %d bytes (limit %d)", candidate.rel, candidate.size, readLimit))
		out.Incomplete = true
		return
	}
	if out.TotalBytes+candidate.size > req.MaxTotalBytes {
		out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: total input limit reached", candidate.rel))
		out.Incomplete = true
		return
	}
	if result.readErr != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: %v", candidate.rel, result.readErr))
		out.Incomplete = true
		return
	}
	if result.media {
		if candidate.size > req.MaxMediaBytes {
			out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: media is %d bytes (limit %d)", candidate.rel, candidate.size, req.MaxMediaBytes))
			out.Incomplete = true
			return
		}
		if len(out.Assets) >= req.MaxMediaCount {
			out.Warnings = append(out.Warnings, "skip "+candidate.rel+": media count limit reached")
			out.Incomplete = true
			return
		}
		if result.inspectErr != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: %v", candidate.rel, result.inspectErr))
			out.Incomplete = true
			return
		}
		if result.asset.Document != nil && result.asset.Document.Pages > req.MaxPages {
			out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: %d pages exceeds limit %d", candidate.rel, result.asset.Document.Pages, req.MaxPages))
			out.Incomplete = true
			return
		}
		if result.asset.Audio != nil && time.Duration(result.asset.Audio.DurationMillis)*time.Millisecond > req.MaxDuration {
			out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: duration exceeds %s", candidate.rel, req.MaxDuration))
			out.Incomplete = true
			return
		}
		if extWarning := extensionMIMEWarning(candidate.rel, result.asset.DetectedMIME); extWarning != "" {
			result.asset.Warnings = append(result.asset.Warnings, extWarning)
			out.Warnings = append(out.Warnings, extWarning)
		}
		for _, warning := range result.asset.Warnings {
			out.Warnings = append(out.Warnings, candidate.rel+": "+warning)
		}
		out.Assets = append(out.Assets, result.asset)
		out.Sources = append(out.Sources, Source{Label: candidate.rel, Path: candidate.rel, Kind: result.asset.SourceKind, SHA256: result.sha, Bytes: candidate.size, AssetID: result.asset.ID, DetectedMIME: result.asset.DetectedMIME, Content: result.content, ExtractedText: normalizeText(result.extracted)})
		out.TotalBytes += candidate.size
		return
	}
	if candidate.size > req.MaxFileBytes {
		out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: text file is %d bytes (limit %d)", candidate.rel, candidate.size, req.MaxFileBytes))
		out.Incomplete = true
		return
	}
	if isBinary(result.content) {
		out.Warnings = append(out.Warnings, "skip binary: "+candidate.rel)
		return
	}
	if isGenerated(candidate.rel, result.content) {
		out.Warnings = append(out.Warnings, "skip generated: "+candidate.rel)
		return
	}
	b := normalizeText(result.content)
	h := sha256.Sum256(b)
	out.Sources = append(out.Sources, Source{Label: candidate.rel, Path: candidate.rel, Kind: "file", SHA256: hex.EncodeToString(h[:]), Bytes: int64(len(b)), Content: b})
	out.TotalBytes += int64(len(b))
}

func boundedWorkers(workers int) int {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		return 1
	}
	return workers
}

func extensionMIMEWarning(path, mime string) string {
	ext := strings.ToLower(filepath.Ext(path))
	want := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif", ".wav": "audio/wav", ".mp3": "audio/mpeg", ".aac": "audio/aac", ".m4a": "audio/mp4", ".flac": "audio/flac", ".ogg": "audio/ogg", ".pdf": "application/pdf", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation", ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}[ext]
	if want != "" && want != mime {
		return fmt.Sprintf("%s: extension suggests %s but content is %s", path, want, mime)
	}
	return ""
}

func attachTranscripts(root string, req CollectRequest, out *Collection) error {
	for _, mapping := range req.Transcripts {
		parts := strings.SplitN(mapping, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return fmt.Errorf("--transcript must be <audio-path>=<text-path>")
		}
		audioPath, textPath := filepath.Clean(parts[0]), filepath.Clean(parts[1])
		if !filepath.IsAbs(textPath) {
			textPath = filepath.Join(root, textPath)
		}
		// #nosec G304 -- reading a caller-named transcript file is the documented purpose of --transcript.
		b, err := os.ReadFile(textPath)
		if err != nil {
			return fmt.Errorf("read transcript %s: %w", parts[1], err)
		}
		if int64(len(b)) > req.MaxFileBytes {
			return fmt.Errorf("transcript %s exceeds text file limit", parts[1])
		}
		if isBinary(b) {
			return fmt.Errorf("transcript %s is not text", parts[1])
		}
		matched := false
		for i := range out.Sources {
			if out.Sources[i].Kind != "audio" {
				continue
			}
			if filepath.Clean(out.Sources[i].Path) == audioPath || filepath.Clean(filepath.Join(root, out.Sources[i].Path)) == filepath.Clean(filepath.Join(root, audioPath)) {
				out.Sources[i].ExtractedText = normalizeText(b)
				matched = true
				for j := range out.Assets {
					if out.Assets[j].ID == out.Sources[i].AssetID {
						out.Assets[j].TranscriptPath = displayPath(root, textPath)
					}
				}
			}
		}
		if !matched {
			return fmt.Errorf("transcript audio path %q was not collected", parts[0])
		}
	}
	return nil
}

func requestedPaths(ctx context.Context, root string, req CollectRequest) ([]string, error) {
	var paths []string
	for _, raw := range req.Paths {
		p := raw
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		if strings.ContainsAny(raw, "*?[") {
			matches, err := filepath.Glob(p)
			if err != nil {
				return nil, fmt.Errorf("invalid glob %q: %w", raw, err)
			}
			if len(matches) == 0 {
				return nil, fmt.Errorf("glob %q matched no files", raw)
			}
			paths = append(paths, matches...)
		} else {
			paths = append(paths, p)
		}
	}
	switch req.Preset {
	case "":
	case "plan":
		found := false
		for _, name := range []string{"plan.md", "PLAN.md"} {
			p := filepath.Join(root, name)
			if _, err := os.Stat(p); err == nil {
				paths = append(paths, p)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("plan preset: no plan.md found in %s", root)
		}
	case "tracked":
		listed, err := gitFiles(ctx, root, "ls-files", "-co", "--exclude-standard")
		if err != nil {
			return nil, err
		}
		paths = append(paths, listed...)
	case "changed":
		changed, err := gitFiles(ctx, root, "diff", "--name-only", "HEAD")
		if err != nil {
			changed, err = gitFiles(ctx, root, "diff", "--name-only")
			if err != nil {
				return nil, err
			}
		}
		untracked, _ := gitFiles(ctx, root, "ls-files", "--others", "--exclude-standard")
		paths = append(paths, changed...)
		paths = append(paths, untracked...)
	default:
		return nil, fmt.Errorf("unknown preset %q", req.Preset)
	}
	if len(paths) == 0 && req.Preset == "" && req.Prompt == "" && req.PromptFile == "" && !req.ReadStdin {
		paths = []string{root}
	}
	sort.Strings(paths)
	return paths, nil
}

func gitFiles(ctx context.Context, root string, args ...string) ([]string, error) {
	// #nosec G204 -- args are compile-time literals from the preset switch; root is
	// passed as a -C operand to git, not to a shell.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git preset failed: %w", err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			out = append(out, filepath.Join(root, filepath.FromSlash(line)))
		}
	}
	return out, nil
}

func normalizeText(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
}

func isBinary(b []byte) bool {
	if len(b) > 8192 {
		b = b[:8192]
	}
	return bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b)
}

func isGenerated(path string, b []byte) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	if strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".min.css") || strings.HasSuffix(lower, ".map") {
		return true
	}
	if len(b) > 4096 {
		b = b[:4096]
	}
	s := strings.ToLower(string(b))
	return strings.Contains(s, "code generated") && strings.Contains(s, "do not edit") || strings.Contains(s, "@generated")
}

func displayPath(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(p)
}

func shouldSkipDir(rel string, includeHidden bool) bool {
	base := filepath.Base(rel)
	switch base {
	case ".git", "node_modules", "vendor", "dist", "build", "coverage", ".next", ".cache":
		return true
	}
	return !includeHidden && strings.HasPrefix(base, ".")
}

func loadIgnoreRules(root string) []ignoreRule {
	var rules []ignoreRule
	for _, name := range []string{".gitignore", ".tokeneyesignore"} {
		// #nosec G304 -- name is one of two literals joined onto the collection root.
		f, err := os.Open(filepath.Join(root, name))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			negate := strings.HasPrefix(line, "!")
			if negate {
				line = strings.TrimPrefix(line, "!")
			}
			if re, err := regexp.Compile(ignorePattern(line)); err == nil {
				rules = append(rules, ignoreRule{re: re, negate: negate})
			}
		}
		_ = f.Close()
	}
	return rules
}

func ignored(path string, rules []ignoreRule) bool {
	path = filepath.ToSlash(strings.TrimPrefix(path, "./"))
	result := false
	for _, r := range rules {
		if r.re.MatchString(path) {
			result = !r.negate
		}
	}
	return result
}

func ignorePattern(pattern string) string {
	pattern = filepath.ToSlash(pattern)
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	dir := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	var b strings.Builder
	if anchored {
		b.WriteString("^")
	} else {
		b.WriteString("^(?:.*/)?")
	}
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	if dir {
		b.WriteString("(?:/.*)?")
	}
	b.WriteString("$")
	return b.String()
}
