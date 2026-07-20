package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polera/tokeneyes/pkg/tokeneyes"
)

func TestSplitKeepsGroupsTogetherAndFeaturesAreContentFree(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	splitPath := filepath.Join(dir, "split.json")
	featuresPath := filepath.Join(dir, "features.json")
	m := manifest{Version: "test-v1", Samples: []sample{
		{ID: "a", Content: "private-a", License: "CC0-1.0", Provenance: "synthetic:test", Kind: "prose", LengthBucket: "tiny", Group: "same"},
		{ID: "b", Content: "private-b", License: "CC0-1.0", Provenance: "synthetic:test", Kind: "code", LengthBucket: "tiny", Group: "same"},
		{ID: "c", Content: "private-c", License: "CC0-1.0", Provenance: "synthetic:test", Kind: "json", LengthBucket: "tiny", Group: "other"},
	}}
	writeFixtureJSON(t, manifestPath, m)
	if err := runSplit([]string{"--manifest", manifestPath, "--out", splitPath, "--seed", "fixed"}); err != nil {
		t.Fatal(err)
	}
	var split manifest
	readFixtureJSON(t, splitPath, &split)
	if split.Samples[0].Split == "" || split.Samples[0].Split != split.Samples[1].Split {
		t.Fatalf("group was split: %+v", split.Samples)
	}
	if err := runFeatures([]string{"--manifest", splitPath, "--out", featuresPath}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(featuresPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private-") || !strings.Contains(string(b), tokenFeatureSchemaFragment()) {
		t.Fatalf("feature artifact leaked content or omitted schema: %s", b)
	}
}

func TestLabelRequiresConsentAndIsResumable(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	labelsPath := filepath.Join(dir, "labels.json")
	m := manifest{Version: "test-v1", Samples: []sample{{ID: "one", Content: "do not retain me", License: "CC0-1.0", Provenance: "synthetic:test", Kind: "prose", LengthBucket: "tiny", Group: "g", Split: "train"}}}
	writeFixtureJSON(t, manifestPath, m)
	if err := runLabel([]string{"--manifest", manifestPath, "--out", labelsPath, "--model", "claude-pinned"}); err == nil || !strings.Contains(err.Error(), "consent") {
		t.Fatalf("label without consent err=%v", err)
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("x-api-key") != "secret-test-key" || r.URL.Path != "/v1/messages/count_tokens" {
			t.Errorf("unexpected request headers/path")
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":7}`))
	}))
	defer server.Close()
	t.Setenv("ANTHROPIC_API_KEY", "secret-test-key")
	args := []string{"--manifest", manifestPath, "--out", labelsPath, "--model", "claude-pinned", "--base-url", server.URL, "--consent-send-to-anthropic"}
	if err := runLabel(args); err != nil {
		t.Fatal(err)
	}
	if err := runLabel(args); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 { // baseline + sample, then baseline-only resume
		t.Fatalf("calls=%d want 3", calls.Load())
	}
	b, err := os.ReadFile(labelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-test-key") || strings.Contains(string(b), "do not retain me") {
		t.Fatalf("label artifact retained secret/content: %s", b)
	}
	var labels []labelRecord
	if err := json.Unmarshal(b, &labels); err != nil || len(labels) != 1 || labels[0].InputTokens != 7 {
		t.Fatalf("labels=%+v err=%v", labels, err)
	}
}

func TestFitEvaluateAndVerifyCandidate(t *testing.T) {
	dir := t.TempDir()
	labelsPath := filepath.Join(dir, "labels.json")
	artifactPath := filepath.Join(dir, "artifact.json")
	reportPath := filepath.Join(dir, "report.json")
	markdownPath := filepath.Join(dir, "report.md")
	var labels []labelRecord
	for i := 1; i <= 18; i++ {
		split := "train"
		if i > 12 {
			split = "validation"
		}
		if i > 16 {
			split = "blind-test"
		}
		content := []byte(strings.Repeat("a", i*4))
		labels = append(labels, labelRecord{
			featureRecord: featureRecord{ID: string(rune('a' + i)), SHA256: strings.Repeat("a", 63) + string(rune('a'+i%6)), Kind: "synthetic", Track: "text-component", LengthBucket: "short", Group: "group-" + string(rune('a'+i)), Split: split, RequestShapeVersion: requestShapeVersion, Features: tokeneyes.ExtractClaudeFeatures(content)},
			PinnedModelID: "claude-pinned", AnthropicAPIVersion: "2023-06-01", CollectedAt: time.Date(2026, 7, 19, 0, 0, i, 0, time.UTC), InputTokens: int64(i + 2), BaselineTokens: 2, AdjustedTokens: int64(i),
		})
	}
	writeFixtureJSON(t, labelsPath, labels)
	if err := runFit([]string{"--labels", labelsPath, "--out", artifactPath, "--family", "legacy", "--artifact-version", "candidate-test-v1", "--features", "byte_count"}); err != nil {
		t.Fatal(err)
	}
	a, _, err := readArtifact(artifactPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "candidate" || len(a.Coefficients) == 0 || len(a.Metrics) < 3 {
		t.Fatalf("unexpected fitted artifact: %+v", a)
	}
	if err := runEvaluate([]string{"--artifact", artifactPath, "--labels", labelsPath, "--include-blind-test", "--json-out", reportPath, "--markdown-out", markdownPath}); err != nil {
		t.Fatal(err)
	}
	if err := runVerifyArtifact([]string{"--artifact", artifactPath, "--labels", labelsPath}); err != nil {
		t.Fatal(err)
	}
	markdown, err := os.ReadFile(markdownPath)
	if err != nil || !strings.Contains(string(markdown), "blind-test") {
		t.Fatalf("markdown report missing blind test: %s err=%v", markdown, err)
	}
}

func writeFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, value); err != nil {
		t.Fatal(err)
	}
}

func tokenFeatureSchemaFragment() string { return `"schema_version": "claude-features-v1"` }
