package tokeneyes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const ClaudeFeatureSchemaVersion = "claude-features-v1"

var claudeCodeKeywords = [...][]byte{
	[]byte("func"), []byte("function"), []byte("class"), []byte("const"),
	[]byte("var"), []byte("let"), []byte("package"), []byte("import"), []byte("def"),
}

// ClaudeFeatures is the versioned, shared training/serving feature schema.
// Every field is deterministic and can be extracted in linear time.
type ClaudeFeatures struct {
	SchemaVersion      string `json:"schema_version"`
	ByteCount          int64  `json:"byte_count"`
	RuneCount          int64  `json:"rune_count"`
	ASCIILetters       int64  `json:"ascii_letters"`
	ASCIIDigits        int64  `json:"ascii_digits"`
	ASCIIUnderscores   int64  `json:"ascii_underscores"`
	Whitespace         int64  `json:"whitespace"`
	Newlines           int64  `json:"newlines"`
	Punctuation        int64  `json:"punctuation"`
	Symbols            int64  `json:"symbols"`
	ASCIIPunctuation   int64  `json:"ascii_punctuation_symbols"`
	ASCIIControlsOther int64  `json:"ascii_controls_other"`
	NonASCII           int64  `json:"non_ascii"`
	Latin              int64  `json:"latin"`
	Han                int64  `json:"han"`
	Cyrillic           int64  `json:"cyrillic"`
	Arabic             int64  `json:"arabic"`
	CombiningMarks     int64  `json:"combining_marks"`
	EmojiSymbols       int64  `json:"emoji_symbols"`
	Words              int64  `json:"words"`
	AlphanumericRuns   int64  `json:"alphanumeric_runs"`
	AlphanumericRunes  int64  `json:"alphanumeric_runes"`
	WhitespaceRuns     int64  `json:"whitespace_runs"`
	MaxAlphanumericRun int64  `json:"max_alphanumeric_run"`
	MaxWhitespaceRun   int64  `json:"max_whitespace_run"`
	Lines              int64  `json:"lines"`
	CodeSignals        int64  `json:"code_signals"`
	StructuredSignals  int64  `json:"structured_signals"`
	InvalidUTF8        bool   `json:"invalid_utf8"`
}

// ExtractClaudeFeatures is used by both the offline estimator and calibration
// tooling, preventing training-serving skew.
func ExtractClaudeFeatures(content []byte) ClaudeFeatures {
	f := ClaudeFeatures{SchemaVersion: ClaudeFeatureSchemaVersion, ByteCount: int64(len(content))}
	if len(content) == 0 {
		return f
	}
	f.InvalidUTF8 = !utf8.Valid(content)
	f.Lines = 1
	var alnumRun, whitespaceRun int64
	inAlnum, inWhitespace := false, false
	for offset := 0; offset < len(content); {
		r, size := utf8.DecodeRune(content[offset:])
		offset += size
		f.RuneCount++
		isAlnum := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
		isSpace := unicode.IsSpace(r)
		if isAlnum {
			if !inAlnum {
				alnumRun = 0
				f.Words++
				f.AlphanumericRuns++
			}
			alnumRun++
			f.AlphanumericRunes++
			if alnumRun > f.MaxAlphanumericRun {
				f.MaxAlphanumericRun = alnumRun
			}
		} else {
			alnumRun = 0
		}
		inAlnum = isAlnum
		if isSpace {
			if !inWhitespace {
				whitespaceRun = 0
				f.WhitespaceRuns++
			}
			whitespaceRun++
			if whitespaceRun > f.MaxWhitespaceRun {
				f.MaxWhitespaceRun = whitespaceRun
			}
		} else {
			whitespaceRun = 0
		}
		inWhitespace = isSpace

		switch {
		case r <= unicode.MaxASCII && unicode.IsLetter(r):
			f.ASCIILetters++
		case r <= unicode.MaxASCII && unicode.IsDigit(r):
			f.ASCIIDigits++
		case r == '_':
			f.ASCIIUnderscores++
		case isSpace:
			f.Whitespace++
			if r == '\n' {
				f.Newlines++
				f.Lines++
			}
		case unicode.IsPunct(r):
			f.Punctuation++
		case unicode.IsSymbol(r):
			f.Symbols++
		}
		if r <= unicode.MaxASCII && !isAlnum && !isSpace {
			if unicode.IsPunct(r) || unicode.IsSymbol(r) {
				f.ASCIIPunctuation++
			} else {
				f.ASCIIControlsOther++
			}
		}
		if r > unicode.MaxASCII {
			f.NonASCII++
		}
		if unicode.In(r, unicode.Latin) {
			f.Latin++
		}
		if unicode.In(r, unicode.Han) {
			f.Han++
		}
		if unicode.In(r, unicode.Cyrillic) {
			f.Cyrillic++
		}
		if unicode.In(r, unicode.Arabic) {
			f.Arabic++
		}
		if unicode.IsMark(r) {
			f.CombiningMarks++
		}
		if unicode.Is(unicode.So, r) && r > unicode.MaxASCII {
			f.EmojiSymbols++
		}
		switch r {
		case '{', '}', '[', ']', ':', ',', '<', '>', '=':
			f.StructuredSignals++
		}
		switch r {
		case '{', '}', '[', ']', '(', ')', ';', ':', '=', '<', '>':
			f.CodeSignals++
		}
	}
	f.CodeSignals += countCodeKeywords(content)
	return f
}

func countCodeKeywords(content []byte) int64 {
	var count int64
	for _, word := range claudeCodeKeywords {
		for offset := 0; offset+len(word) <= len(content); {
			relative := bytes.Index(content[offset:], word)
			if relative < 0 {
				break
			}
			start := offset + relative
			end := start + len(word)
			if (start == 0 || !asciiWordByte(content[start-1])) && (end == len(content) || !asciiWordByte(content[end])) {
				count++
			}
			offset = end
		}
	}
	return count
}

func asciiWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// Vector returns the numeric feature vector consumed by estimator artifacts.
func (f ClaudeFeatures) Vector() map[string]float64 {
	v := map[string]float64{
		"byte_count": float64(f.ByteCount), "rune_count": float64(f.RuneCount),
		"ascii_letters": float64(f.ASCIILetters), "ascii_digits": float64(f.ASCIIDigits),
		"ascii_underscores": float64(f.ASCIIUnderscores), "whitespace": float64(f.Whitespace),
		"newlines": float64(f.Newlines), "punctuation": float64(f.Punctuation),
		"symbols": float64(f.Symbols), "ascii_punctuation_symbols": float64(f.ASCIIPunctuation), "ascii_controls_other": float64(f.ASCIIControlsOther), "non_ascii": float64(f.NonASCII), "latin": float64(f.Latin),
		"han": float64(f.Han), "cyrillic": float64(f.Cyrillic), "arabic": float64(f.Arabic),
		"combining_marks": float64(f.CombiningMarks), "emoji_symbols": float64(f.EmojiSymbols),
		"words": float64(f.Words), "alphanumeric_runs": float64(f.AlphanumericRuns), "alphanumeric_runes": float64(f.AlphanumericRunes),
		"whitespace_runs": float64(f.WhitespaceRuns), "max_alphanumeric_run": float64(f.MaxAlphanumericRun),
		"max_whitespace_run": float64(f.MaxWhitespaceRun), "lines": float64(f.Lines),
		"code_signals": float64(f.CodeSignals), "structured_signals": float64(f.StructuredSignals),
		"average_alphanumeric_run": 0, "average_whitespace_run": 0, "average_line_bytes": 0,
		"code_density": 0, "structured_density": 0,
	}
	if f.AlphanumericRuns > 0 {
		v["average_alphanumeric_run"] = float64(f.AlphanumericRunes) / float64(f.AlphanumericRuns)
	}
	if f.WhitespaceRuns > 0 {
		v["average_whitespace_run"] = float64(f.Whitespace) / float64(f.WhitespaceRuns)
	}
	if f.Lines > 0 {
		v["average_line_bytes"] = float64(f.ByteCount) / float64(f.Lines)
	}
	if f.RuneCount > 0 {
		v["code_density"] = float64(f.CodeSignals) / float64(f.RuneCount)
		v["structured_density"] = float64(f.StructuredSignals) / float64(f.RuneCount)
	}
	if f.InvalidUTF8 {
		v["invalid_utf8"] = 1
	} else {
		v["invalid_utf8"] = 0
	}
	return v
}

// ClaudeEstimatorArtifact is the immutable runtime representation exported by
// the calibration tool after evaluation. Production callers should require
// Accepted to prevent candidate artifacts from being mislabeled as calibrated.
type ClaudeEstimatorArtifact struct {
	ArtifactVersion       string                  `json:"artifact_version"`
	FeatureSchemaVersion  string                  `json:"feature_schema_version"`
	TokenizerFamily       string                  `json:"tokenizer_family"`
	Status                string                  `json:"status"`
	PinnedReferenceModels []string                `json:"pinned_reference_models"`
	AnthropicAPIVersion   string                  `json:"anthropic_api_version"`
	ManifestDigests       map[string]string       `json:"manifest_digests"`
	Intercept             float64                 `json:"intercept"`
	Coefficients          map[string]float64      `json:"coefficients"`
	ResidualBounds        ClaudeResidualBounds    `json:"residual_bounds"`
	OOD                   ClaudeOODRules          `json:"out_of_distribution"`
	Metrics               map[string]ClaudeMetric `json:"metrics"`
	CalibrationDate       string                  `json:"calibration_date"`
	ProvenanceURLs        []string                `json:"provenance_urls"`
}

type ClaudeResidualBounds struct {
	RelativeLow  float64 `json:"relative_low"`
	RelativeHigh float64 `json:"relative_high"`
	Coverage     float64 `json:"coverage"`
}

type ClaudeOODRules struct {
	Maximums          map[string]float64 `json:"maximums"`
	BoundMultiplier   float64            `json:"bound_multiplier"`
	ConfidencePenalty float64            `json:"confidence_penalty"`
}

type ClaudeMetric struct {
	Samples                 int     `json:"samples"`
	IndependentGroups       int     `json:"independent_groups"`
	MAPE                    float64 `json:"mape"`
	BaselineMAPE            float64 `json:"baseline_mape,omitempty"`
	MacroMAPE               float64 `json:"macro_mape"`
	BaselineMacroMAPE       float64 `json:"baseline_macro_mape,omitempty"`
	MacroMAPEImprovement    float64 `json:"macro_mape_improvement,omitempty"`
	P95AbsolutePercentError float64 `json:"p95_absolute_percentage_error"`
	SignedPercentBias       float64 `json:"signed_percentage_bias"`
	Coverage                float64 `json:"coverage"`
	OfficialAboveHigh       float64 `json:"official_above_high"`
}

func ParseClaudeEstimatorArtifact(data []byte, requireAccepted bool) (ClaudeEstimatorArtifact, error) {
	var a ClaudeEstimatorArtifact
	if err := json.Unmarshal(data, &a); err != nil {
		return a, fmt.Errorf("parse Claude estimator artifact: %w", err)
	}
	if err := a.Validate(requireAccepted); err != nil {
		return a, err
	}
	return a, nil
}

func (a ClaudeEstimatorArtifact) Validate(requireAccepted bool) error {
	if a.ArtifactVersion == "" || a.FeatureSchemaVersion != ClaudeFeatureSchemaVersion || (a.TokenizerFamily != "legacy" && a.TokenizerFamily != "new") {
		return fmt.Errorf("invalid Claude estimator artifact identity")
	}
	if a.Status != "candidate" && a.Status != "accepted" {
		return fmt.Errorf("claude estimator artifact %s has invalid status %q", a.ArtifactVersion, a.Status)
	}
	if requireAccepted && a.Status != "accepted" {
		return fmt.Errorf("claude estimator artifact %s has status %q, not accepted", a.ArtifactVersion, a.Status)
	}
	if len(a.PinnedReferenceModels) == 0 || a.AnthropicAPIVersion == "" || len(a.ManifestDigests) < 3 || len(a.ProvenanceURLs) == 0 {
		return fmt.Errorf("claude estimator artifact %s lacks reproducibility metadata", a.ArtifactVersion)
	}
	if _, err := time.Parse(time.DateOnly, a.CalibrationDate); err != nil {
		return fmt.Errorf("claude estimator artifact %s has invalid calibration date", a.ArtifactVersion)
	}
	if len(a.Coefficients) == 0 || !finite(a.Intercept) || !finite(a.ResidualBounds.RelativeLow) || !finite(a.ResidualBounds.RelativeHigh) || !finite(a.ResidualBounds.Coverage) || a.ResidualBounds.RelativeLow < -1 || a.ResidualBounds.RelativeLow > 0 || a.ResidualBounds.RelativeHigh < 0 || a.ResidualBounds.Coverage <= 0 || a.ResidualBounds.Coverage > 1 {
		return fmt.Errorf("claude estimator artifact %s has invalid model parameters", a.ArtifactVersion)
	}
	if !finite(a.OOD.BoundMultiplier) || a.OOD.BoundMultiplier < 1 || !finite(a.OOD.ConfidencePenalty) || a.OOD.ConfidencePenalty < 0 || a.OOD.ConfidencePenalty > 1 {
		return fmt.Errorf("claude estimator artifact %s has invalid out-of-distribution rules", a.ArtifactVersion)
	}
	for name, maximum := range a.OOD.Maximums {
		if !finite(maximum) || maximum < 0 {
			return fmt.Errorf("claude estimator artifact %s has invalid OOD maximum %q", a.ArtifactVersion, name)
		}
	}
	valid := ExtractClaudeFeatures(nil).Vector()
	for name, coefficient := range a.Coefficients {
		if _, ok := valid[name]; !ok || math.IsNaN(coefficient) || math.IsInf(coefficient, 0) {
			return fmt.Errorf("claude estimator artifact %s has invalid coefficient %q", a.ArtifactVersion, name)
		}
	}
	for name, metric := range a.Metrics {
		if metric.Samples < 0 || metric.IndependentGroups < 0 || !finite(metric.MAPE) || !finite(metric.BaselineMAPE) || !finite(metric.MacroMAPE) || !finite(metric.BaselineMacroMAPE) || !finite(metric.MacroMAPEImprovement) || !finite(metric.P95AbsolutePercentError) || !finite(metric.SignedPercentBias) || !finite(metric.Coverage) || !finite(metric.OfficialAboveHigh) || metric.Coverage < 0 || metric.Coverage > 1 || metric.OfficialAboveHigh < 0 || metric.OfficialAboveHigh > 1 {
			return fmt.Errorf("claude estimator artifact %s has invalid metric %q", a.ArtifactVersion, name)
		}
	}
	if a.Status == "accepted" {
		blind, ok := a.Metrics["blind-test"]
		if !ok || blind.Samples == 0 || blind.MacroMAPEImprovement < .20 || blind.SignedPercentBias < -.02 || blind.SignedPercentBias > .05 || blind.Coverage < .95 || blind.OfficialAboveHigh > .05 {
			return fmt.Errorf("claude estimator artifact %s does not satisfy blind-test acceptance gates", a.ArtifactVersion)
		}
		maxMAPE, maxP95 := .05, .15
		if a.TokenizerFamily == "new" || a.TokenizerFamily == "claude-new" {
			maxMAPE, maxP95 = .08, .20
		}
		if blind.MAPE > maxMAPE || blind.P95AbsolutePercentError > maxP95 {
			return fmt.Errorf("claude estimator artifact %s exceeds family error gates", a.ArtifactVersion)
		}
		for name, metric := range a.Metrics {
			if strings.HasPrefix(name, "stratum:") && metric.Samples > 0 && metric.Coverage < .90 {
				return fmt.Errorf("claude estimator artifact %s has insufficient coverage for %s", a.ArtifactVersion, name)
			}
		}
	}
	return nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func (a ClaudeEstimatorArtifact) Predict(content []byte) (Count, error) {
	return a.PredictFeatures(ExtractClaudeFeatures(content), true)
}

// PredictFeatures evaluates a previously extracted vector. Calibration tools
// may set requireAccepted=false while evaluating a candidate; production must
// always require an accepted artifact.
func (a ClaudeEstimatorArtifact) PredictFeatures(f ClaudeFeatures, requireAccepted bool) (Count, error) {
	if err := a.Validate(requireAccepted); err != nil {
		return Count{}, err
	}
	method := "calibrated:" + a.TokenizerFamily + ":" + a.ArtifactVersion
	if f.ByteCount == 0 {
		return Count{Method: method, FormulaVersion: a.ArtifactVersion, Confidence: a.ResidualBounds.Coverage}, nil
	}
	v := f.Vector()
	prediction := a.Intercept
	for _, name := range ClaudeFeatureNames() {
		if coefficient, ok := a.Coefficients[name]; ok {
			prediction += coefficient * v[name]
		}
	}
	if prediction < 1 {
		prediction = 1
	}
	expected := clampTokenFloat(math.Floor(prediction + .5))
	ood := f.InvalidUTF8
	for name, maximum := range a.OOD.Maximums {
		if maximum >= 0 && v[name] > maximum {
			ood = true
		}
	}
	lowRate, highRate := a.ResidualBounds.RelativeLow, a.ResidualBounds.RelativeHigh
	confidence := a.ResidualBounds.Coverage
	if ood {
		multiplier := a.OOD.BoundMultiplier
		if multiplier < 1 {
			multiplier = 1
		}
		lowRate *= multiplier
		highRate *= multiplier
		confidence -= a.OOD.ConfidencePenalty
		if confidence < 0 {
			confidence = 0
		}
	}
	low := clampTokenFloat(math.Floor(float64(expected) * (1 + lowRate)))
	high := clampTokenFloat(math.Ceil(float64(expected) * (1 + highRate)))
	if low < 1 {
		low = 1
	}
	if high < expected {
		high = expected
	}
	return Count{Tokens: expected, Low: low, High: high, Method: method, FormulaVersion: a.ArtifactVersion, Confidence: confidence}, nil
}

func clampTokenFloat(value float64) int64 {
	if math.IsNaN(value) || value <= 0 {
		return 0
	}
	if math.IsInf(value, 1) || value >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(value)
}

func estimateClaudeHeuristic(content []byte, family string) Count {
	method, scale, errorRate, confidence := "heuristic:claude-v1", 1.0, .15, 0.0
	if family == "new" {
		method, scale, errorRate, confidence = "heuristic:claude-new-tokenizer-v1", 1.30, .25, 0.0
	}
	if len(content) == 0 {
		return Count{Method: method, FormulaVersion: ClaudeFeatureSchemaVersion + ":heuristic-v1", Confidence: confidence}
	}
	f := ExtractClaudeFeatures(content)
	if f.InvalidUTF8 {
		return boundedClaudeHeuristic(float64(len(content))/3, method, scale, errorRate, confidence)
	}
	value := float64(f.ASCIILetters+f.ASCIIDigits+f.ASCIIUnderscores)/4.05 + float64(f.ASCIIPunctuation+f.ASCIIControlsOther)*.72 + float64(f.NonASCII)*.76 + float64(f.Whitespace)/18
	if f.CodeSignals > 0 {
		value *= 1.07
	}
	return boundedClaudeHeuristic(value, method, scale, errorRate, confidence)
}

func boundedClaudeHeuristic(value float64, method string, scale, errorRate, confidence float64) Count {
	n := clampTokenFloat(math.Ceil(value * scale))
	if n < 1 {
		n = 1
	}
	low := clampTokenFloat(math.Floor(float64(n) * (1 - errorRate)))
	high := clampTokenFloat(math.Ceil(float64(n) * (1 + errorRate)))
	if low < 1 {
		low = 1
	}
	return Count{Tokens: n, Low: low, High: high, Method: method, FormulaVersion: ClaudeFeatureSchemaVersion + ":heuristic-v1", Confidence: confidence}
}

func ClaudeFeatureNames() []string {
	v := ExtractClaudeFeatures(nil).Vector()
	out := make([]string, 0, len(v))
	for name := range v {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
