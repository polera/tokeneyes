package tokeneyes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type fixedVerifier struct {
	tokens int64
	err    error
}

func (v fixedVerifier) Verify(context.Context, Model, AssembledRequest) (VerificationResult, error) {
	return VerificationResult{Tokens: v.tokens, Method: "fixture:official", Transport: "inline", CleanupStatus: "not_required"}, v.err
}

func TestEngineSeparatesWrapperOverheadAndDropsContent(t *testing.T) {
	content := []byte("private sentinel")
	collection := Collection{Sources: []Source{{Label: "prompt", Kind: "prompt", SHA256: "hash", Bytes: int64(len(content)), Content: content}}}
	e := NewEngine(DefaultCatalog())
	e.Now = func() time.Time { return time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC) }
	run, err := e.Analyze(context.Background(), AnalyzeRequest{Collection: collection, Models: []string{"gpt-5.5"}, OutputTokens: []int64{1000}})
	if err != nil {
		t.Fatal(err)
	}
	result := run.Results[0]
	if result.Overhead.WrapperTokens <= 0 {
		t.Fatalf("wrapper overhead not separated: %+v", result.Overhead)
	}
	if result.InputTokens != result.Sources[0].Count.Tokens+result.Overhead.Total() {
		t.Fatalf("input categories do not add up: %+v", result)
	}
	b, _ := json.Marshal(run)
	if strings.Contains(string(b), "private sentinel") {
		t.Fatal("run JSON retained source content")
	}
}

func TestEngineVerificationExactAndFailClosed(t *testing.T) {
	collection := Collection{Sources: []Source{{Label: "x", Kind: "prompt", Content: []byte("hello"), Bytes: 5}}}
	e := NewEngine(DefaultCatalog())
	e.Verifier = fixedVerifier{tokens: 123}
	run, err := e.Analyze(context.Background(), AnalyzeRequest{Collection: collection, Models: []string{"claude"}, Verify: true, RequireVerify: true, OutputTokens: []int64{0}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Results[0].Verification.Tokens != 123 || run.Results[0].InputTokens == 123 {
		t.Fatalf("verification must be recorded without replacing local estimate: %+v", run.Results[0])
	}
	e.Verifier = fixedVerifier{err: errors.New("no credential")}
	if _, err := e.Analyze(context.Background(), AnalyzeRequest{Collection: collection, Models: []string{"claude"}, Verify: true, RequireVerify: true}); err == nil {
		t.Fatal("required verification did not fail closed")
	}
}
