package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polera/tokeneyes/pkg/tokeneyes"
)

func testApp(t *testing.T, stdin string) (*App, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	root := t.TempDir()
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	return &App{Stdout: stdout, Stderr: stderr, Stdin: strings.NewReader(stdin), Getwd: func() (string, error) { return root, nil }}, stdout, stderr, root
}

func TestEstimateImageJSONIncludesPrivacySafePlan(t *testing.T) {
	app, stdout, stderr, root := testApp(t, "")
	pngData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "pixel.png"), pngData, 0o600); err != nil {
		t.Fatal(err)
	}
	code := app.Execute(context.Background(), []string{"estimate", "pixel.png", "--model", "gpt-5.6", "--image-detail", "original", "--json", "--no-save"})
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, `"detected_mime": "image/png"`) || !strings.Contains(got, `"modality": "image"`) || strings.Contains(got, base64.StdEncoding.EncodeToString(pngData)) {
		t.Fatalf("unexpected mixed JSON: %s", got)
	}
}

func TestEstimateJSONAndInterspersedFlags(t *testing.T) {
	app, stdout, stderr, root := testApp(t, "")
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := app.Execute(context.Background(), []string{"estimate", "input.txt", "--model", "gpt-5.5", "--json", "--no-save"})
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"schema_version": "tokeneyes.run.v2"`) || strings.Contains(stdout.String(), "hello world") {
		t.Fatalf("unexpected JSON: %s", stdout.String())
	}
}

func TestThresholdExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "tokens", args: []string{"estimate", "--prompt", "hello", "--max-input-tokens", "1", "--no-save"}, want: ExitTokenBudget},
		{name: "cost", args: []string{"estimate", "--prompt", "hello", "--output-tokens", "1", "--max-cost-usd", "0", "--no-save"}, want: ExitCostBudget},
		{name: "verification", args: []string{"estimate", "--prompt", "hello", "--verify", "--require-verification", "--no-save"}, want: ExitVerification},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, _, _, _ := testApp(t, "")
			if got := app.Execute(context.Background(), tt.args); got != tt.want {
				t.Fatalf("code=%d want=%d", got, tt.want)
			}
		})
	}
}

func TestStdinAndConfigFlagOverride(t *testing.T) {
	app, stdout, stderr, root := testApp(t, "from stdin")
	config := filepath.Join(root, "custom.yaml")
	if err := os.WriteFile(config, []byte("model: claude-sonnet-4-6\noutput_tokens: [10]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := app.Execute(context.Background(), []string{"estimate", "--config", config, "--stdin", "--model", "gpt-5.4-mini", "--no-save"})
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "gpt-5.4-mini") || strings.Contains(stdout.String(), "claude-sonnet") {
		t.Fatalf("flag did not override config: %s", stdout.String())
	}
}

func TestCompareTUI(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("COLUMNS", "64")
	app, stdout, stderr, _ := testApp(t, "")
	code := app.Execute(context.Background(), []string{
		"compare", "--prompt", "hello world",
		"--models", "gpt-5.5,claude-sonnet-4-6,gemini-3.5-flash",
		"--output-tokens", "4000", "--tui", "--no-save",
	})
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"Scanning 1 source", "11 B",
		"gpt-5.5", "claude-sonnet-4-6", "gemini-3.5-flash",
		"tok", "% context", "expected", "source content not saved",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("TUI output missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "gpt-5.5") > strings.Index(got, "claude-sonnet-4-6") ||
		strings.Index(got, "claude-sonnet-4-6") > strings.Index(got, "gemini-3.5-flash") {
		t.Fatalf("TUI did not preserve requested model order:\n%s", got)
	}
	if strings.Contains(got, "\x1b[") || strings.Contains(got, "hello world") {
		t.Fatalf("TUI emitted color or source content with NO_COLOR set:\n%s", got)
	}
}

func TestTUIAndJSONAreMutuallyExclusive(t *testing.T) {
	app, _, stderr, _ := testApp(t, "")
	code := app.Execute(context.Background(), []string{"estimate", "--prompt", "hello", "--tui", "--json", "--no-save"})
	if code != ExitUsage {
		t.Fatalf("code=%d want=%d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "--json and --tui cannot be used together") {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestHumanOutputSummarizesWarnings(t *testing.T) {
	run := warningTestRun()

	var standard bytes.Buffer
	printRun(&standard, run, true)
	assertWarningSummary(t, standard.String())

	t.Setenv("NO_COLOR", "1")
	t.Setenv("COLUMNS", "120")
	var tui bytes.Buffer
	printTUI(&tui, run, true)
	assertWarningSummary(t, tui.String())
}

func TestHistoryRunShowsAllWarnings(t *testing.T) {
	app, stdout, stderr, root := testApp(t, "")
	db := filepath.Join(root, "history.db")
	store, err := tokeneyes.OpenSQLite(db)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(context.Background(), warningTestRun()); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	code := app.Execute(context.Background(), []string{"history", "run-with-warnings", "--db", db})
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	got := stdout.String()
	for _, warning := range []string{"first warning detail", "second warning detail"} {
		if !strings.Contains(got, warning) {
			t.Errorf("history output missing %q:\n%s", warning, got)
		}
	}
	if strings.Count(got, "first warning detail") != 1 {
		t.Errorf("history output did not deduplicate warnings:\n%s", got)
	}
}

func warningTestRun() tokeneyes.Run {
	return tokeneyes.Run{
		ID:             "run-with-warnings",
		CreatedAt:      time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
		CatalogVersion: "test-catalog",
		Config:         tokeneyes.RunConfig{Models: []string{"test-model"}},
		Warnings:       []string{"first warning detail"},
		Results: []tokeneyes.ModelResult{{
			Model:            "test-model",
			CapabilityStatus: "supported",
			Warnings:         []string{"first warning detail", "second warning detail"},
		}},
	}
}

func assertWarningSummary(t *testing.T, got string) {
	t.Helper()
	for _, want := range []string{"2 warnings", "tokeneyes history run-with-warnings"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	for _, detail := range []string{"first warning detail", "second warning detail"} {
		if strings.Contains(got, detail) {
			t.Errorf("output included warning detail %q:\n%s", detail, got)
		}
	}
}
