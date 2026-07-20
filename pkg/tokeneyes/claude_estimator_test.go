package tokeneyes

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClaudeFeatureGolden(t *testing.T) {
	f := ExtractClaudeFeatures([]byte("Go_42!\n世界 e\u0301 🚀"))
	if f.SchemaVersion != ClaudeFeatureSchemaVersion || f.ByteCount != 22 || f.RuneCount != 14 {
		t.Fatalf("unexpected size features: %+v", f)
	}
	if f.ASCIILetters != 3 || f.ASCIIDigits != 2 || f.ASCIIUnderscores != 1 || f.ASCIIPunctuation != 1 {
		t.Fatalf("unexpected ASCII classes: %+v", f)
	}
	if f.Han != 2 || f.CombiningMarks != 1 || f.EmojiSymbols != 1 || f.Newlines != 1 || f.Lines != 2 {
		t.Fatalf("unexpected Unicode/line classes: %+v", f)
	}
	if f.Words != 3 || f.MaxAlphanumericRun != 5 {
		t.Fatalf("unexpected run features: %+v", f)
	}
}

func TestClaudeArtifactValidationPredictionAndOOD(t *testing.T) {
	a := acceptedTestArtifact()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseClaudeEstimatorArtifact(b, true)
	if err != nil {
		t.Fatal(err)
	}
	inDistribution, err := parsed.Predict([]byte("abcd"))
	if err != nil {
		t.Fatal(err)
	}
	ood, err := parsed.Predict([]byte(strings.Repeat("a", 40)))
	if err != nil {
		t.Fatal(err)
	}
	if inDistribution.Tokens != 1 || inDistribution.Low > inDistribution.Tokens || inDistribution.High < inDistribution.Tokens {
		t.Fatalf("bad deterministic prediction: %+v", inDistribution)
	}
	if ood.Confidence >= inDistribution.Confidence || ood.High < 14 || !strings.Contains(ood.Method, a.ArtifactVersion) {
		t.Fatalf("OOD rules were not applied: in=%+v ood=%+v", inDistribution, ood)
	}
	empty, err := parsed.Predict(nil)
	if err != nil || empty.Tokens != 0 || empty.Low != 0 || empty.High != 0 {
		t.Fatalf("empty prediction=%+v err=%v", empty, err)
	}
}

func TestClaudeCandidateCannotServeProduction(t *testing.T) {
	a := acceptedTestArtifact()
	a.Status = "candidate"
	b, _ := json.Marshal(a)
	if _, err := ParseClaudeEstimatorArtifact(b, true); err == nil {
		t.Fatal("candidate artifact was accepted for production")
	}
	if _, err := ParseClaudeEstimatorArtifact(b, false); err != nil {
		t.Fatalf("candidate should be inspectable by calibration tooling: %v", err)
	}
}

func TestCatalogTokenizerIDSelectsAcceptedArtifact(t *testing.T) {
	a := acceptedTestArtifact()
	counter, err := NewLocalCounterWithClaudeArtifacts(a)
	if err != nil {
		t.Fatal(err)
	}
	model := Model{ID: "pinned-model", Provider: "anthropic", Tokenizer: a.ArtifactVersion}
	got, err := counter.Count(model, []byte("abcdefgh"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Tokens != 2 || got.FormulaVersion != a.ArtifactVersion {
		t.Fatalf("catalog tokenizer did not select artifact: %+v", got)
	}
	a.Status = "candidate"
	if _, err := NewLocalCounterWithClaudeArtifacts(a); err == nil {
		t.Fatal("candidate artifact entered production counter")
	}
}

func TestClaudeInvalidUTF8IsHandledAndMarkedOOD(t *testing.T) {
	f := ExtractClaudeFeatures([]byte{0xff, 'a'})
	if !f.InvalidUTF8 || f.ByteCount != 2 {
		t.Fatalf("invalid UTF-8 was not explicit: %+v", f)
	}
	got := estimateClaudeHeuristic([]byte{0xff, 'a'}, "legacy")
	if got.Tokens < 1 || got.Low > got.Tokens || got.High < got.Tokens {
		t.Fatalf("invalid UTF-8 estimate violated bounds: %+v", got)
	}
}

func TestClaudeArtifactPredictionClampsOverflow(t *testing.T) {
	a := acceptedTestArtifact()
	a.Coefficients["byte_count"] = 1e308
	got, err := a.Predict([]byte(strings.Repeat("x", 40)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Tokens <= 0 || got.Low > got.Tokens || got.Tokens > got.High {
		t.Fatalf("overflow clamp violated bounds: %+v", got)
	}
}

func FuzzClaudeEstimatorBounds(f *testing.F) {
	f.Add([]byte("hello 世界"))
	f.Add([]byte{0xff, 0x00, '\n'})
	a := acceptedTestArtifact()
	f.Fuzz(func(t *testing.T, content []byte) {
		for _, family := range []string{"legacy", "new"} {
			got := estimateClaudeHeuristic(content, family)
			if got.Low > got.Tokens || got.Tokens > got.High || got.Low < 0 {
				t.Fatalf("heuristic invariant failed: %+v", got)
			}
		}
		got, err := a.Predict(content)
		if err != nil {
			t.Fatal(err)
		}
		if got.Low > got.Tokens || got.Tokens > got.High || got.Low < 0 {
			t.Fatalf("artifact invariant failed: %+v", got)
		}
	})
}

func BenchmarkExtractClaudeFeatures(b *testing.B) {
	b.StopTimer()
	content := []byte(strings.Repeat("func example_42() { return \"世界\" }\n", 1000))
	b.ReportAllocs()
	b.SetBytes(int64(len(content)))
	b.StartTimer()
	for range b.N {
		_ = ExtractClaudeFeatures(content)
	}
}

func acceptedTestArtifact() ClaudeEstimatorArtifact {
	return ClaudeEstimatorArtifact{
		ArtifactVersion:       "claude-calibrated-legacy-test-v1",
		FeatureSchemaVersion:  ClaudeFeatureSchemaVersion,
		TokenizerFamily:       "legacy",
		Status:                "accepted",
		PinnedReferenceModels: []string{"claude-pinned-test"},
		AnthropicAPIVersion:   "2023-06-01",
		ManifestDigests:       map[string]string{"train": "a", "validation": "b", "blind-test": "c"},
		Coefficients:          map[string]float64{"byte_count": .25},
		ResidualBounds:        ClaudeResidualBounds{RelativeLow: -.1, RelativeHigh: .2, Coverage: .95},
		OOD:                   ClaudeOODRules{Maximums: map[string]float64{"byte_count": 10}, BoundMultiplier: 2, ConfidencePenalty: .1},
		Metrics:               map[string]ClaudeMetric{"blind-test": {Samples: 100, IndependentGroups: 20, MAPE: .04, BaselineMAPE: .08, MacroMAPE: .04, BaselineMacroMAPE: .08, MacroMAPEImprovement: .5, P95AbsolutePercentError: .12, SignedPercentBias: .01, Coverage: .96, OfficialAboveHigh: .04}},
		CalibrationDate:       "2026-07-19",
		ProvenanceURLs:        []string{"https://example.test/calibration"},
	}
}
