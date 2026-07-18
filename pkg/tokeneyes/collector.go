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
	"sort"
	"strings"
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

	for _, p := range paths {
		select {
		case <-ctx.Done():
			return Collection{}, ctx.Err()
		default:
		}
		abs, absErr := filepath.Abs(p)
		if absErr != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: %v", p, absErr))
			out.Incomplete = true
			continue
		}
		info, statErr := os.Lstat(abs)
		if statErr != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: %v", p, statErr))
			out.Incomplete = true
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			out.Warnings = append(out.Warnings, "skip symlink: "+displayPath(root, abs))
			continue
		}
		if info.IsDir() {
			walkErr := filepath.WalkDir(abs, func(candidate string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: %v", displayPath(root, candidate), walkErr))
					out.Incomplete = true
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
				collectFile(candidate, root, req, seen, &out, addBytes)
				return nil
			})
			if walkErr != nil {
				out.Warnings = append(out.Warnings, walkErr.Error())
				out.Incomplete = true
			}
			continue
		}
		if ignored(displayPath(root, abs), rules) {
			continue
		}
		collectFile(abs, root, req, seen, &out, addBytes)
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

func collectFile(abs, root string, req CollectRequest, seen map[string]bool, out *Collection, add func(string, string, string, []byte)) {
	if seen[abs] {
		return
	}
	seen[abs] = true
	rel := displayPath(root, abs)
	info, err := os.Lstat(abs)
	if err != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: %v", rel, err))
		out.Incomplete = true
		return
	}
	if !info.Mode().IsRegular() {
		return
	}
	readLimit := req.MaxFileBytes
	if req.MaxMediaBytes > readLimit {
		readLimit = req.MaxMediaBytes
	}
	if info.Size() > readLimit {
		out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: file is %d bytes (limit %d)", rel, info.Size(), readLimit))
		out.Incomplete = true
		return
	}
	if out.TotalBytes+info.Size() > req.MaxTotalBytes {
		out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: total input limit reached", rel))
		out.Incomplete = true
		return
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: %v", rel, err))
		out.Incomplete = true
		return
	}
	h := sha256.Sum256(b)
	sha := hex.EncodeToString(h[:])
	asset, extracted, media, inspectErr := inspectMedia(rel, rel, sha, b)
	if media {
		if info.Size() > req.MaxMediaBytes {
			out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: media is %d bytes (limit %d)", rel, info.Size(), req.MaxMediaBytes))
			out.Incomplete = true
			return
		}
		if len(out.Assets) >= req.MaxMediaCount {
			out.Warnings = append(out.Warnings, "skip "+rel+": media count limit reached")
			out.Incomplete = true
			return
		}
		if inspectErr != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: %v", rel, inspectErr))
			out.Incomplete = true
			return
		}
		if asset.Document != nil && asset.Document.Pages > req.MaxPages {
			out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: %d pages exceeds limit %d", rel, asset.Document.Pages, req.MaxPages))
			out.Incomplete = true
			return
		}
		if asset.Audio != nil && time.Duration(asset.Audio.DurationMillis)*time.Millisecond > req.MaxDuration {
			out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: duration exceeds %s", rel, req.MaxDuration))
			out.Incomplete = true
			return
		}
		if extWarning := extensionMIMEWarning(rel, asset.DetectedMIME); extWarning != "" {
			asset.Warnings = append(asset.Warnings, extWarning)
			out.Warnings = append(out.Warnings, extWarning)
		}
		for _, warning := range asset.Warnings {
			out.Warnings = append(out.Warnings, rel+": "+warning)
		}
		out.Assets = append(out.Assets, asset)
		out.Sources = append(out.Sources, Source{Label: rel, Path: rel, Kind: asset.SourceKind, SHA256: sha, Bytes: info.Size(), AssetID: asset.ID, DetectedMIME: asset.DetectedMIME, Content: b, ExtractedText: normalizeText(extracted)})
		out.TotalBytes += info.Size()
		return
	}
	if info.Size() > req.MaxFileBytes {
		out.Warnings = append(out.Warnings, fmt.Sprintf("skip %s: text file is %d bytes (limit %d)", rel, info.Size(), req.MaxFileBytes))
		out.Incomplete = true
		return
	}
	if isBinary(b) {
		out.Warnings = append(out.Warnings, "skip binary: "+rel)
		return
	}
	if isGenerated(rel, b) {
		out.Warnings = append(out.Warnings, "skip generated: "+rel)
		return
	}
	add(rel, rel, "file", b)
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
