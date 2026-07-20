// Command anthropic-calibration builds reproducible Claude estimator artifacts.
// It is developer tooling: ordinary TokenEyes builds and tests never invoke it.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/polera/tokeneyes/pkg/tokeneyes"
)

const requestShapeVersion = "anthropic-minimal-message-v1"

type manifest struct {
	Version string   `json:"version"`
	Samples []sample `json:"samples"`
}

type sample struct {
	ID           string `json:"id"`
	Path         string `json:"path,omitempty"`
	Content      string `json:"content,omitempty"`
	License      string `json:"license"`
	Provenance   string `json:"provenance"`
	Kind         string `json:"kind"`
	Language     string `json:"language,omitempty"`
	LengthBucket string `json:"length_bucket"`
	Group        string `json:"group"`
	Split        string `json:"split,omitempty"`
	Track        string `json:"track,omitempty"`
	System       string `json:"system,omitempty"`
	Tools        any    `json:"tools,omitempty"`
}

type structuredFeatures struct {
	MessageCount     int64 `json:"message_count"`
	SystemBlockCount int64 `json:"system_block_count"`
	ToolSchemaCount  int64 `json:"tool_schema_count"`
	WrapperCount     int64 `json:"wrapper_count"`
}

type featureRecord struct {
	ID                  string                   `json:"id"`
	SHA256              string                   `json:"sha256"`
	Kind                string                   `json:"kind"`
	Track               string                   `json:"track"`
	Language            string                   `json:"language,omitempty"`
	LengthBucket        string                   `json:"length_bucket"`
	Group               string                   `json:"group"`
	Split               string                   `json:"split"`
	RequestShapeVersion string                   `json:"request_shape_version"`
	Features            tokeneyes.ClaudeFeatures `json:"features"`
	Structured          structuredFeatures       `json:"structured_features"`
}

type retryRecord struct {
	At         time.Time `json:"at"`
	StatusCode int       `json:"status_code,omitempty"`
	WaitMS     int64     `json:"wait_ms"`
	Error      string    `json:"error,omitempty"`
}

type labelRecord struct {
	featureRecord
	PinnedModelID       string        `json:"pinned_model_id"`
	AnthropicAPIVersion string        `json:"anthropic_api_version"`
	CollectedAt         time.Time     `json:"collected_at"`
	InputTokens         int64         `json:"input_tokens"`
	BaselineTokens      int64         `json:"baseline_tokens"`
	AdjustedTokens      int64         `json:"baseline_adjusted_tokens"`
	RetryHistory        []retryRecord `json:"retry_history,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: anthropic-calibration features|split|label|fit|evaluate|verify-artifact [flags]")
	}
	var err error
	switch os.Args[1] {
	case "features":
		err = runFeatures(os.Args[2:])
	case "split":
		err = runSplit(os.Args[2:])
	case "label":
		err = runLabel(os.Args[2:])
	case "fit":
		err = runFit(os.Args[2:])
	case "evaluate":
		err = runEvaluate(os.Args[2:])
	case "verify-artifact":
		err = runVerifyArtifact(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func runFeatures(args []string) error {
	fs := flag.NewFlagSet("features", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "", "corpus manifest JSON")
	out := fs.String("out", "", "feature-record JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, base, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	records, err := extractRecords(m, base)
	if err != nil {
		return err
	}
	return writeJSONAtomic(*out, records)
}

func runSplit(args []string) error {
	fs := flag.NewFlagSet("split", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "", "input corpus manifest JSON")
	out := fs.String("out", "", "split corpus manifest JSON")
	seed := fs.String("seed", "tokeneyes-anthropic-v1", "fixed split seed")
	train := fs.Int("train-percent", 70, "training group percentage")
	validation := fs.Int("validation-percent", 15, "validation group percentage")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *train <= 0 || *validation <= 0 || *train+*validation >= 100 {
		return errors.New("split percentages must leave non-zero train, validation, and blind-test sets")
	}
	m, _, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	for i := range m.Samples {
		h := sha256.Sum256([]byte(*seed + "\x00" + m.Samples[i].Group))
		bucket := int(h[0]) * 100 / 256
		switch {
		case bucket < *train:
			m.Samples[i].Split = "train"
		case bucket < *train+*validation:
			m.Samples[i].Split = "validation"
		default:
			m.Samples[i].Split = "blind-test"
		}
	}
	return writeJSONAtomic(*out, m)
}

func runLabel(args []string) error {
	fs := flag.NewFlagSet("label", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "", "split corpus manifest JSON")
	out := fs.String("out", "", "resumable label JSON output")
	model := fs.String("model", "", "pinned Anthropic model ID")
	apiVersion := fs.String("api-version", "2023-06-01", "Anthropic API version header")
	baseURL := fs.String("base-url", "https://api.anthropic.com", "Anthropic API base URL")
	consent := fs.Bool("consent-send-to-anthropic", false, "confirm that manifest content may be sent to Anthropic")
	maxRetries := fs.Int("max-retries", 5, "retry limit for rate limits and transient errors")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*consent {
		return errors.New("label requires --consent-send-to-anthropic; corpus content will be sent to Anthropic")
	}
	if *model == "" || strings.Contains(*model, "latest") {
		return errors.New("--model must be a pinned Anthropic model ID, not an alias")
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return errors.New("ANTHROPIC_API_KEY is not set")
	}
	m, base, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	features, err := extractRecords(m, base)
	if err != nil {
		return err
	}
	existing, err := readLabelsIfExists(*out)
	if err != nil {
		return err
	}
	done := make(map[string]labelRecord, len(existing))
	for _, r := range existing {
		if r.PinnedModelID != *model || r.AnthropicAPIVersion != *apiVersion {
			return fmt.Errorf("existing output was collected for a different model or API version")
		}
		done[r.ID] = r
	}
	fmt.Fprintln(os.Stderr, "anthropic-calibration: explicit consent recorded; sending corpus content to Anthropic's token-count endpoint")
	client := &http.Client{Timeout: 60 * time.Second}
	baseline, baselineRetries, err := countAnthropic(context.Background(), client, *baseURL, key, *apiVersion, *model, sample{}, *maxRetries)
	if err != nil {
		return fmt.Errorf("measure minimal-request baseline: %w", err)
	}
	byID := make(map[string]sample, len(m.Samples))
	for _, s := range m.Samples {
		byID[s.ID] = s
	}
	for _, f := range features {
		if prior, ok := done[f.ID]; ok {
			if prior.SHA256 != f.SHA256 || prior.RequestShapeVersion != f.RequestShapeVersion {
				return fmt.Errorf("sample %s changed since it was labeled", f.ID)
			}
			continue
		}
		s := byID[f.ID]
		content, readErr := sampleContent(s, base)
		if readErr != nil {
			return readErr
		}
		s.Content = string(content)
		count, history, countErr := countAnthropic(context.Background(), client, *baseURL, key, *apiVersion, *model, s, *maxRetries)
		if countErr != nil {
			return fmt.Errorf("label %s: %w", s.ID, countErr)
		}
		adjusted := count - baseline
		if adjusted < 0 {
			adjusted = 0
		}
		retryHistory := append([]retryRecord(nil), baselineRetries...)
		retryHistory = append(retryHistory, history...)
		existing = append(existing, labelRecord{featureRecord: f, PinnedModelID: *model, AnthropicAPIVersion: *apiVersion, CollectedAt: time.Now().UTC(), InputTokens: count, BaselineTokens: baseline, AdjustedTokens: adjusted, RetryHistory: retryHistory})
		if err := writeJSONAtomic(*out, existing); err != nil {
			return err
		}
	}
	return nil
}

func countAnthropic(ctx context.Context, client *http.Client, baseURL, key, apiVersion, model string, s sample, maxRetries int) (int64, []retryRecord, error) {
	payload := map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": s.Content}}}
	if s.System != "" {
		payload["system"] = s.System
	}
	if s.Tools != nil {
		payload["tools"] = s.Tools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	var history []retryRecord
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/messages/count_tokens", bytes.NewReader(body))
		if err != nil {
			return 0, history, err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", apiVersion)
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var result struct {
				InputTokens int64 `json:"input_tokens"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
			closeErr := resp.Body.Close()
			if decodeErr != nil {
				return 0, history, decodeErr
			}
			if result.InputTokens < 0 {
				return 0, history, errors.New("anthropic returned a negative token count")
			}
			return result.InputTokens, history, closeErr
		}
		status := 0
		retryable := err != nil
		if resp != nil {
			status = resp.StatusCode
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			retryable = status == http.StatusTooManyRequests || status >= 500
		}
		if !retryable || attempt >= maxRetries {
			if err != nil {
				return 0, history, err
			}
			return 0, history, fmt.Errorf("anthropic returned HTTP %d", status)
		}
		wait := time.Duration(1<<min(attempt, 5)) * time.Second
		if resp != nil {
			if seconds, parseErr := strconv.Atoi(resp.Header.Get("retry-after")); parseErr == nil && seconds > 0 {
				wait = time.Duration(seconds) * time.Second
			}
		}
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		record := retryRecord{At: time.Now().UTC(), StatusCode: status, WaitMS: wait.Milliseconds()}
		if err != nil {
			record.Error = err.Error()
		}
		history = append(history, record)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, history, ctx.Err()
		case <-timer.C:
		}
	}
}

func readManifest(path string) (manifest, string, error) {
	var m manifest
	if path == "" {
		return m, "", errors.New("--manifest is required")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- developer-supplied corpus manifest.
	if err != nil {
		return m, "", err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, "", err
	}
	if m.Version == "" || len(m.Samples) == 0 {
		return m, "", errors.New("manifest requires a version and at least one sample")
	}
	seen := make(map[string]bool, len(m.Samples))
	for i := range m.Samples {
		s := &m.Samples[i]
		if s.ID == "" || s.Group == "" || s.Kind == "" || s.License == "" || s.Provenance == "" || s.LengthBucket == "" {
			return m, "", fmt.Errorf("sample %q lacks required metadata", s.ID)
		}
		if seen[s.ID] {
			return m, "", fmt.Errorf("duplicate sample ID %q", s.ID)
		}
		if (s.Path == "") == (s.Content == "") {
			return m, "", fmt.Errorf("sample %s must set exactly one of path or content", s.ID)
		}
		if s.Track == "" {
			s.Track = "text-component"
		}
		if s.Track != "text-component" && s.Track != "structured-request" {
			return m, "", fmt.Errorf("sample %s has invalid labeling track %q", s.ID, s.Track)
		}
		if s.Track == "text-component" && (s.System != "" || s.Tools != nil) {
			return m, "", fmt.Errorf("text-component sample %s cannot contain system or tool blocks", s.ID)
		}
		seen[s.ID] = true
	}
	return m, filepath.Dir(path), nil
}

func extractRecords(m manifest, base string) ([]featureRecord, error) {
	records := make([]featureRecord, 0, len(m.Samples))
	groups := make(map[string]string)
	for _, s := range m.Samples {
		if prior, ok := groups[s.Group]; ok && prior != s.Split {
			return nil, fmt.Errorf("group %q appears in both %q and %q", s.Group, prior, s.Split)
		}
		groups[s.Group] = s.Split
		content, err := sampleContent(s, base)
		if err != nil {
			return nil, err
		}
		digest := sampleDigest(s, content)
		records = append(records, featureRecord{ID: s.ID, SHA256: digest, Kind: s.Kind, Track: s.Track, Language: s.Language, LengthBucket: s.LengthBucket, Group: s.Group, Split: s.Split, RequestShapeVersion: requestShapeVersion, Features: tokeneyes.ExtractClaudeFeatures(content), Structured: requestFeatures(s)})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
}

func sampleDigest(s sample, content []byte) string {
	if s.Track == "text-component" {
		digest := sha256.Sum256(content)
		return hex.EncodeToString(digest[:])
	}
	h := sha256.New()
	_, _ = h.Write(content)
	_, _ = h.Write([]byte("\x00" + s.System + "\x00"))
	tools, _ := json.Marshal(s.Tools)
	_, _ = h.Write(tools)
	return hex.EncodeToString(h.Sum(nil))
}

func requestFeatures(s sample) structuredFeatures {
	f := structuredFeatures{MessageCount: 1, WrapperCount: 1}
	if s.System != "" {
		f.SystemBlockCount = 1
	}
	if tools, ok := s.Tools.([]any); ok {
		f.ToolSchemaCount = int64(len(tools))
	} else if s.Tools != nil {
		f.ToolSchemaCount = 1
	}
	return f
}

func sampleContent(s sample, base string) ([]byte, error) {
	if s.Path == "" {
		return []byte(s.Content), nil
	}
	path := filepath.Join(base, filepath.Clean(s.Path))
	b, err := os.ReadFile(path) // #nosec G304 -- path is explicitly declared by the developer corpus manifest.
	if err != nil {
		return nil, fmt.Errorf("sample %s: %w", s.ID, err)
	}
	return b, nil
}

func readLabelsIfExists(path string) ([]labelRecord, error) {
	if path == "" {
		return nil, errors.New("--out is required")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- developer-supplied result path.
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []labelRecord
	return records, json.Unmarshal(b, &records)
}

func writeJSONAtomic(path string, value any) error {
	if path == "" {
		return errors.New("--out is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".anthropic-calibration-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err = enc.Encode(value); err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "anthropic-calibration: "+format+"\n", args...)
	os.Exit(1)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
