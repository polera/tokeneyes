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
	if run.Results[0].DecisionInputTokens != 123 {
		t.Fatalf("official verification was not authoritative for decisions: %+v", run.Results[0])
	}
	e.Verifier = fixedVerifier{err: errors.New("no credential")}
	if _, err := e.Analyze(context.Background(), AnalyzeRequest{Collection: collection, Models: []string{"claude"}, Verify: true, RequireVerify: true}); err == nil {
		t.Fatal("required verification did not fail closed")
	}
}

func TestEstimateBoundSelectsHighForDecisionsAndCosts(t *testing.T) {
	collection := Collection{Sources: []Source{{Label: "x", Kind: "prompt", Content: []byte(strings.Repeat("hello world ", 100)), Bytes: 1200}}}
	e := NewEngine(DefaultCatalog())
	e.Now = func() time.Time { return time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC) }
	expected, err := e.Analyze(context.Background(), AnalyzeRequest{Collection: collection, Models: []string{"claude-sonnet-4-6"}, OutputTokens: []int64{0}, EstimateBound: "expected"})
	if err != nil {
		t.Fatal(err)
	}
	high, err := e.Analyze(context.Background(), AnalyzeRequest{Collection: collection, Models: []string{"claude-sonnet-4-6"}, OutputTokens: []int64{0}, EstimateBound: "high"})
	if err != nil {
		t.Fatal(err)
	}
	er, hr := expected.Results[0], high.Results[0]
	if er.DecisionInputTokens != er.InputTokens || hr.DecisionInputTokens != hr.InputHigh || hr.DecisionInputTokens <= er.DecisionInputTokens {
		t.Fatalf("bound selection failed: expected=%+v high=%+v", er, hr)
	}
	if high.Config.EstimateBound != "high" || hr.Scenarios[0].CostMicrosUSD < er.Scenarios[0].CostMicrosUSD {
		t.Fatalf("high bound was not persisted/priced: expected=%+v high=%+v", er.Scenarios[0], hr.Scenarios[0])
	}
}
