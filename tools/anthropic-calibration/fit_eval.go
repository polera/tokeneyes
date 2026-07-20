package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/polera/tokeneyes/pkg/tokeneyes"
)

var defaultFitFeatures = []string{
	"byte_count", "rune_count", "words", "ascii_punctuation_symbols", "punctuation", "symbols",
	"non_ascii", "whitespace", "code_signals", "structured_signals",
}

type evaluationReport struct {
	ArtifactVersion string                            `json:"artifact_version"`
	Family          string                            `json:"tokenizer_family"`
	GeneratedAt     time.Time                         `json:"generated_at"`
	Metrics         map[string]tokeneyes.ClaudeMetric `json:"metrics"`
	ByStratum       map[string]tokeneyes.ClaudeMetric `json:"by_stratum"`
	Digest          string                            `json:"label_digest"`
}

func runFit(args []string) error {
	fs := flag.NewFlagSet("fit", flag.ContinueOnError)
	labelsPath := fs.String("labels", "", "label JSON produced by label")
	out := fs.String("out", "", "candidate estimator artifact JSON")
	family := fs.String("family", "", "tokenizer family identifier")
	version := fs.String("artifact-version", "", "immutable artifact version")
	featureList := fs.String("features", strings.Join(defaultFitFeatures, ","), "comma-separated feature names")
	ridge := fs.Float64("ridge", 1e-6, "ridge penalty for non-intercept coefficients")
	provenance := fs.String("provenance", "https://blog.gopenai.com/counting-claude-tokens-without-a-tokenizer-e767f2b6e632,https://github.com/petasbytes/token-approx", "comma-separated provenance URLs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *family == "" || *version == "" {
		return errors.New("--family and --artifact-version are required")
	}
	if *family != "legacy" && *family != "new" {
		return errors.New("--family must be legacy or new")
	}
	if *ridge < 0 {
		return errors.New("--ridge cannot be negative")
	}
	labels, err := readLabels(*labelsPath)
	if err != nil {
		return err
	}
	features, err := validateFeatureNames(splitNonempty(*featureList))
	if err != nil {
		return err
	}
	train := selectSplit(labels, "train")
	validation := selectSplit(labels, "validation")
	if len(train) <= len(features) || len(validation) == 0 {
		return fmt.Errorf("need more than %d training samples and a non-empty validation split", len(features))
	}
	candidates, err := fitCandidates(train, validation, features, *ridge, *family)
	if err != nil {
		return err
	}
	selected := selectCandidate(candidates, *family)
	intercept, coefficients := selected.intercept, selected.coefficients
	predict := func(r labelRecord) float64 { return predictVector(intercept, coefficients, r.Features.Vector()) }
	low, high := residualQuantileBounds(validation, predict)
	coverage := intervalCoverage(validation, predict, low, high)
	models := uniqueModels(labels)
	apiVersion, err := uniqueAPIVersion(labels)
	if err != nil {
		return err
	}
	digests := splitDigests(labels)
	if len(digests) != 3 {
		return errors.New("labels must include train, validation, and blind-test splits before fitting")
	}
	metrics := map[string]tokeneyes.ClaudeMetric{
		"train":      withBaseline(calculateMetric(train, predict, low, high), heuristicMetric(train, *family)),
		"validation": withBaseline(calculateMetric(validation, predict, low, high), heuristicMetric(validation, *family)),
	}
	for _, candidate := range candidates {
		metrics["candidate:"+candidate.name+":validation"] = candidate.validation
	}
	a := tokeneyes.ClaudeEstimatorArtifact{
		ArtifactVersion:       *version,
		FeatureSchemaVersion:  tokeneyes.ClaudeFeatureSchemaVersion,
		TokenizerFamily:       *family,
		Status:                "candidate",
		PinnedReferenceModels: models,
		AnthropicAPIVersion:   apiVersion,
		ManifestDigests:       digests,
		Intercept:             intercept,
		Coefficients:          coefficients,
		ResidualBounds:        tokeneyes.ClaudeResidualBounds{RelativeLow: low, RelativeHigh: high, Coverage: coverage},
		OOD: tokeneyes.ClaudeOODRules{
			Maximums:          trainingMaximums(train),
			BoundMultiplier:   1.5,
			ConfidencePenalty: .1,
		},
		Metrics:         metrics,
		CalibrationDate: latestCollectionDate(labels),
		ProvenanceURLs:  splitNonempty(*provenance),
	}
	if err := a.Validate(false); err != nil {
		return err
	}
	return writeJSONAtomic(*out, a)
}

type fittedCandidate struct {
	name         string
	intercept    float64
	coefficients map[string]float64
	validation   tokeneyes.ClaudeMetric
}

func fitCandidates(train, validation []labelRecord, features []string, ridge float64, family string) ([]fittedCandidate, error) {
	baseline := heuristicMetric(validation, family)
	var out []fittedCandidate
	add := func(name string, selected []string, penalty float64) error {
		beta, err := fitRidge(train, selected, penalty)
		if err != nil {
			return err
		}
		coefficients := make(map[string]float64, len(selected))
		for i, feature := range selected {
			coefficients[feature] = beta[i+1]
		}
		predict := func(r labelRecord) float64 { return predictVector(beta[0], coefficients, r.Features.Vector()) }
		metric := withBaseline(calculateMetric(validation, predict, -.15, .25), baseline)
		out = append(out, fittedCandidate{name: name, intercept: beta[0], coefficients: coefficients, validation: metric})
		return nil
	}
	// Anthropic's character heuristic is retained as a benchmark baseline.
	char := fittedCandidate{name: "character-heuristic", coefficients: map[string]float64{"rune_count": .25}}
	charPredict := func(r labelRecord) float64 { return predictVector(0, char.coefficients, r.Features.Vector()) }
	char.validation = withBaseline(calculateMetric(validation, charPredict, -.15, .25), baseline)
	out = append(out, char)
	for _, feature := range features {
		if err := add("linear:"+feature, []string{feature}, 0); err != nil {
			continue
		}
	}
	if err := add("ols", features, 0); err != nil {
		// Correlated size features can make OLS singular; ridge remains required.
	}
	if err := add("ridge", features, ridge); err != nil {
		return nil, err
	}
	return out, nil
}

func selectCandidate(candidates []fittedCandidate, family string) fittedCandidate {
	for _, candidate := range candidates {
		if validationGate(candidate.validation, family) {
			return candidate
		}
	}
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.validation.MAPE < best.validation.MAPE {
			best = candidate
		}
	}
	return best
}

func validationGate(metric tokeneyes.ClaudeMetric, family string) bool {
	maxMAPE, maxP95 := .05, .15
	if family == "new" || family == "claude-new" {
		maxMAPE, maxP95 = .08, .20
	}
	return metric.MacroMAPEImprovement >= .20 && metric.MAPE <= maxMAPE && metric.P95AbsolutePercentError <= maxP95 && metric.SignedPercentBias >= -.02 && metric.SignedPercentBias <= .05
}

func withBaseline(metric, baseline tokeneyes.ClaudeMetric) tokeneyes.ClaudeMetric {
	metric.BaselineMAPE = baseline.MAPE
	metric.BaselineMacroMAPE = baseline.MacroMAPE
	if baseline.MacroMAPE > 0 {
		metric.MacroMAPEImprovement = (baseline.MacroMAPE - metric.MacroMAPE) / baseline.MacroMAPE
	}
	return metric
}

func heuristicMetric(records []labelRecord, family string) tokeneyes.ClaudeMetric {
	predict := func(r labelRecord) float64 {
		v := r.Features.Vector()
		p := (v["ascii_letters"]+v["ascii_digits"]+v["ascii_underscores"])/4.05 + (v["ascii_punctuation_symbols"]+v["ascii_controls_other"])*.72 + v["non_ascii"]*.76 + v["whitespace"]/18
		if v["code_signals"] > 0 {
			p *= 1.07
		}
		if family == "new" || family == "claude-new" {
			p *= 1.30
		}
		if p < 1 {
			p = 1
		}
		return math.Ceil(p)
	}
	return calculateMetric(records, predict, -.15, .25)
}

func runEvaluate(args []string) error {
	fs := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	artifactPath := fs.String("artifact", "", "candidate or accepted artifact JSON")
	labelsPath := fs.String("labels", "", "label JSON")
	jsonOut := fs.String("json-out", "", "JSON metrics output")
	markdownOut := fs.String("markdown-out", "", "Markdown metrics output")
	includeBlind := fs.Bool("include-blind-test", false, "evaluate the frozen blind test (release decision only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, artifactBytes, err := readArtifact(*artifactPath, false)
	if err != nil {
		return err
	}
	labels, err := readLabels(*labelsPath)
	if err != nil {
		return err
	}
	predict := func(r labelRecord) float64 {
		return predictVector(a.Intercept, a.Coefficients, r.Features.Vector())
	}
	report := evaluationReport{ArtifactVersion: a.ArtifactVersion, Family: a.TokenizerFamily, GeneratedAt: time.Now().UTC(), Metrics: make(map[string]tokeneyes.ClaudeMetric), ByStratum: make(map[string]tokeneyes.ClaudeMetric), Digest: digestBytes(artifactBytes, mustJSON(labels))}
	for _, split := range []string{"train", "validation"} {
		records := selectSplit(labels, split)
		if len(records) > 0 {
			report.Metrics[split] = withBaseline(calculateMetric(records, predict, a.ResidualBounds.RelativeLow, a.ResidualBounds.RelativeHigh), heuristicMetric(records, a.TokenizerFamily))
		}
	}
	if *includeBlind {
		blind := selectSplit(labels, "blind-test")
		if len(blind) == 0 {
			return errors.New("blind-test split is empty")
		}
		report.Metrics["blind-test"] = withBaseline(calculateMetric(blind, predict, a.ResidualBounds.RelativeLow, a.ResidualBounds.RelativeHigh), heuristicMetric(blind, a.TokenizerFamily))
		for _, key := range stratumKeys(blind) {
			records := selectStratum(blind, key)
			report.ByStratum[key] = withBaseline(calculateMetric(records, predict, a.ResidualBounds.RelativeLow, a.ResidualBounds.RelativeHigh), heuristicMetric(records, a.TokenizerFamily))
		}
	}
	if *jsonOut == "" && *markdownOut == "" {
		return errors.New("at least one of --json-out or --markdown-out is required")
	}
	if *jsonOut != "" {
		if err := writeJSONAtomic(*jsonOut, report); err != nil {
			return err
		}
	}
	if *markdownOut != "" {
		if err := writeTextAtomic(*markdownOut, markdownReport(report)); err != nil {
			return err
		}
	}
	return nil
}

func runVerifyArtifact(args []string) error {
	fs := flag.NewFlagSet("verify-artifact", flag.ContinueOnError)
	artifactPath := fs.String("artifact", "", "artifact JSON")
	labelsPath := fs.String("labels", "", "optional label JSON for digest and metrics checks")
	expectedSHA := fs.String("sha256", "", "optional expected artifact SHA-256")
	requireAccepted := fs.Bool("require-accepted", false, "reject candidate artifacts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, b, err := readArtifact(*artifactPath, *requireAccepted)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(b)
	actualSHA := hex.EncodeToString(digest[:])
	if *expectedSHA != "" && !strings.EqualFold(*expectedSHA, actualSHA) {
		return fmt.Errorf("artifact SHA-256 %s does not match %s", actualSHA, *expectedSHA)
	}
	if *labelsPath != "" {
		labels, err := readLabels(*labelsPath)
		if err != nil {
			return err
		}
		for split, want := range a.ManifestDigests {
			if got := splitDigests(labels)[split]; got != want {
				return fmt.Errorf("%s manifest digest mismatch: got %s want %s", split, got, want)
			}
		}
		predict := func(r labelRecord) float64 { return predictVector(a.Intercept, a.Coefficients, r.Features.Vector()) }
		for split, want := range a.Metrics {
			if strings.HasPrefix(split, "candidate:") || strings.HasPrefix(split, "stratum:") {
				continue
			}
			got := calculateMetric(selectSplit(labels, split), predict, a.ResidualBounds.RelativeLow, a.ResidualBounds.RelativeHigh)
			got = withBaseline(got, heuristicMetric(selectSplit(labels, split), a.TokenizerFamily))
			if !metricsEqual(got, want) {
				return fmt.Errorf("%s metrics are not reproducible", split)
			}
		}
	}
	fmt.Printf("artifact=%s status=%s sha256=%s verified\n", a.ArtifactVersion, a.Status, actualSHA)
	return nil
}

func readLabels(path string) ([]labelRecord, error) {
	if path == "" {
		return nil, errors.New("--labels is required")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- developer-supplied calibration labels.
	if err != nil {
		return nil, err
	}
	var labels []labelRecord
	if err := json.Unmarshal(b, &labels); err != nil {
		return nil, err
	}
	if len(labels) == 0 {
		return nil, errors.New("label file is empty")
	}
	groups := make(map[string]string)
	seen := make(map[string]bool)
	for _, r := range labels {
		if r.ID == "" || seen[r.ID] || r.Features.SchemaVersion != tokeneyes.ClaudeFeatureSchemaVersion || r.RequestShapeVersion != requestShapeVersion || r.SHA256 == "" || r.PinnedModelID == "" || r.Kind == "" || r.Group == "" || (r.Track != "text-component" && r.Track != "structured-request") || (r.Split != "train" && r.Split != "validation" && r.Split != "blind-test") || r.InputTokens < 0 || r.BaselineTokens < 0 || r.AdjustedTokens < 0 || r.CollectedAt.IsZero() {
			return nil, fmt.Errorf("invalid label record %q", r.ID)
		}
		seen[r.ID] = true
		if prior, ok := groups[r.Group]; ok && prior != r.Split {
			return nil, fmt.Errorf("group %q leaks across splits", r.Group)
		}
		groups[r.Group] = r.Split
	}
	return labels, nil
}

func readArtifact(path string, requireAccepted bool) (tokeneyes.ClaudeEstimatorArtifact, []byte, error) {
	if path == "" {
		return tokeneyes.ClaudeEstimatorArtifact{}, nil, errors.New("--artifact is required")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- developer-supplied estimator artifact.
	if err != nil {
		return tokeneyes.ClaudeEstimatorArtifact{}, nil, err
	}
	a, err := tokeneyes.ParseClaudeEstimatorArtifact(b, requireAccepted)
	return a, b, err
}

func validateFeatureNames(names []string) ([]string, error) {
	valid := make(map[string]bool)
	for _, name := range tokeneyes.ClaudeFeatureNames() {
		valid[name] = true
	}
	seen := make(map[string]bool)
	for _, name := range names {
		if !valid[name] {
			return nil, fmt.Errorf("unknown feature %q", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate feature %q", name)
		}
		seen[name] = true
	}
	if len(names) == 0 {
		return nil, errors.New("at least one feature is required")
	}
	return names, nil
}

func fitRidge(records []labelRecord, features []string, ridge float64) ([]float64, error) {
	n := len(features) + 1
	a := make([][]float64, n)
	for i := range a {
		a[i] = make([]float64, n)
	}
	b := make([]float64, n)
	for _, r := range records {
		x := make([]float64, n)
		x[0] = 1
		v := r.Features.Vector()
		for i, name := range features {
			x[i+1] = v[name]
		}
		y := float64(r.AdjustedTokens)
		for i := range n {
			b[i] += x[i] * y
			for j := range n {
				a[i][j] += x[i] * x[j]
			}
		}
	}
	for i := 1; i < n; i++ {
		a[i][i] += ridge
	}
	return solve(a, b)
}

func solve(a [][]float64, b []float64) ([]float64, error) {
	n := len(b)
	for col := range n {
		pivot := col
		for row := col + 1; row < n; row++ {
			if math.Abs(a[row][col]) > math.Abs(a[pivot][col]) {
				pivot = row
			}
		}
		if math.Abs(a[pivot][col]) < 1e-12 {
			return nil, errors.New("singular fit; choose fewer features or a larger ridge penalty")
		}
		a[col], a[pivot] = a[pivot], a[col]
		b[col], b[pivot] = b[pivot], b[col]
		divisor := a[col][col]
		for j := col; j < n; j++ {
			a[col][j] /= divisor
		}
		b[col] /= divisor
		for row := 0; row < n; row++ {
			if row == col {
				continue
			}
			factor := a[row][col]
			for j := col; j < n; j++ {
				a[row][j] -= factor * a[col][j]
			}
			b[row] -= factor * b[col]
		}
	}
	return b, nil
}

func predictVector(intercept float64, coefficients, vector map[string]float64) float64 {
	p := intercept
	names := make([]string, 0, len(coefficients))
	for name := range coefficients {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p += coefficients[name] * vector[name]
	}
	if p < 1 {
		return 1
	}
	return math.Floor(p + .5)
}

func residualQuantileBounds(records []labelRecord, predict func(labelRecord) float64) (float64, float64) {
	residuals := make([]float64, 0, len(records))
	for _, r := range records {
		p := predict(r)
		residuals = append(residuals, (float64(r.AdjustedTokens)-p)/p)
	}
	sort.Float64s(residuals)
	low := quantile(residuals, .025)
	high := quantile(residuals, .975)
	if low > 0 {
		low = 0
	}
	if high < 0 {
		high = 0
	}
	return low, high
}

func calculateMetric(records []labelRecord, predict func(labelRecord) float64, lowRate, highRate float64) tokeneyes.ClaudeMetric {
	if len(records) == 0 {
		return tokeneyes.ClaudeMetric{}
	}
	groups := make(map[string]bool)
	absErrors := make([]float64, 0, len(records))
	stratumError := make(map[string]float64)
	stratumCount := make(map[string]int)
	var signed float64
	covered := 0
	aboveHigh := 0
	for _, r := range records {
		groups[r.Group] = true
		actual, p := float64(r.AdjustedTokens), predict(r)
		denominator := math.Max(actual, 1)
		errorRate := (p - actual) / denominator
		signed += errorRate
		absErrors = append(absErrors, math.Abs(errorRate))
		stratumError[r.Kind] += math.Abs(errorRate)
		stratumCount[r.Kind]++
		if actual >= math.Floor(p*(1+lowRate)) && actual <= math.Ceil(p*(1+highRate)) {
			covered++
		}
		if actual > math.Ceil(p*(1+highRate)) {
			aboveHigh++
		}
	}
	sort.Float64s(absErrors)
	var sum float64
	for _, e := range absErrors {
		sum += e
	}
	var macro float64
	for kind, kindError := range stratumError {
		macro += kindError / float64(stratumCount[kind])
	}
	macro /= float64(len(stratumError))
	return tokeneyes.ClaudeMetric{Samples: len(records), IndependentGroups: len(groups), MAPE: sum / float64(len(records)), MacroMAPE: macro, P95AbsolutePercentError: quantile(absErrors, .95), SignedPercentBias: signed / float64(len(records)), Coverage: float64(covered) / float64(len(records)), OfficialAboveHigh: float64(aboveHigh) / float64(len(records))}
}

func intervalCoverage(records []labelRecord, predict func(labelRecord) float64, low, high float64) float64 {
	return calculateMetric(records, predict, low, high).Coverage
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Ceil(q*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func selectSplit(labels []labelRecord, split string) []labelRecord {
	var out []labelRecord
	for _, r := range labels {
		if r.Split == split {
			out = append(out, r)
		}
	}
	return out
}

func splitDigests(labels []labelRecord) map[string]string {
	parts := make(map[string][]string)
	for _, r := range labels {
		parts[r.Split] = append(parts[r.Split], r.ID+":"+r.SHA256+":"+r.Group)
	}
	out := make(map[string]string)
	for split, values := range parts {
		sort.Strings(values)
		d := sha256.Sum256([]byte(strings.Join(values, "\n")))
		out[split] = hex.EncodeToString(d[:])
	}
	return out
}

func trainingMaximums(records []labelRecord) map[string]float64 {
	out := make(map[string]float64)
	for _, r := range records {
		for name, value := range r.Features.Vector() {
			if value > out[name] {
				out[name] = value
			}
		}
	}
	return out
}

func uniqueModels(labels []labelRecord) []string {
	seen := make(map[string]bool)
	var out []string
	for _, r := range labels {
		if !seen[r.PinnedModelID] {
			seen[r.PinnedModelID] = true
			out = append(out, r.PinnedModelID)
		}
	}
	sort.Strings(out)
	return out
}

func uniqueAPIVersion(labels []labelRecord) (string, error) {
	want := labels[0].AnthropicAPIVersion
	for _, r := range labels[1:] {
		if r.AnthropicAPIVersion != want {
			return "", errors.New("labels contain multiple Anthropic API versions")
		}
	}
	return want, nil
}

func latestCollectionDate(labels []labelRecord) string {
	var latest time.Time
	for _, r := range labels {
		if r.CollectedAt.After(latest) {
			latest = r.CollectedAt
		}
	}
	return latest.Format(time.DateOnly)
}

func stratumKeys(records []labelRecord) []string {
	seen := make(map[string]bool)
	for _, r := range records {
		seen[r.Kind+"/"+r.LengthBucket] = true
	}
	var out []string
	for key := range seen {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func selectStratum(records []labelRecord, key string) []labelRecord {
	var out []labelRecord
	for _, r := range records {
		if r.Kind+"/"+r.LengthBucket == key {
			out = append(out, r)
		}
	}
	return out
}

func markdownReport(r evaluationReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Anthropic estimator evaluation: %s\n\n", r.ArtifactVersion)
	fmt.Fprintf(&b, "Family: `%s`  \nLabel digest: `%s`\n\n", r.Family, r.Digest)
	b.WriteString("| Split/stratum | Samples | Groups | MAPE | Macro MAPE | vs baseline | p95 APE | Bias | Coverage | Above high |\n")
	b.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	keys := make([]string, 0, len(r.Metrics))
	for key := range r.Metrics {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeMetricRow(&b, key, r.Metrics[key])
	}
	keys = keys[:0]
	for key := range r.ByStratum {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeMetricRow(&b, key, r.ByStratum[key])
	}
	return b.String()
}

func writeMetricRow(b *strings.Builder, name string, m tokeneyes.ClaudeMetric) {
	fmt.Fprintf(b, "| %s | %d | %d | %.2f%% | %.2f%% | %+.2f%% | %.2f%% | %+.2f%% | %.2f%% | %.2f%% |\n", name, m.Samples, m.IndependentGroups, 100*m.MAPE, 100*m.MacroMAPE, 100*m.MacroMAPEImprovement, 100*m.P95AbsolutePercentError, 100*m.SignedPercentBias, 100*m.Coverage, 100*m.OfficialAboveHigh)
}

func writeTextAtomic(path, content string) error {
	if path == "" {
		return errors.New("output path is required")
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
	if _, err = f.WriteString(content); err == nil {
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

func splitNonempty(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func digestBytes(values ...[]byte) string {
	h := sha256.New()
	for _, value := range values {
		_, _ = h.Write(value)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func metricsEqual(a, b tokeneyes.ClaudeMetric) bool {
	return a.Samples == b.Samples && a.IndependentGroups == b.IndependentGroups && closeFloat(a.MAPE, b.MAPE) && closeFloat(a.BaselineMAPE, b.BaselineMAPE) && closeFloat(a.MacroMAPE, b.MacroMAPE) && closeFloat(a.BaselineMacroMAPE, b.BaselineMacroMAPE) && closeFloat(a.MacroMAPEImprovement, b.MacroMAPEImprovement) && closeFloat(a.P95AbsolutePercentError, b.P95AbsolutePercentError) && closeFloat(a.SignedPercentBias, b.SignedPercentBias) && closeFloat(a.Coverage, b.Coverage) && closeFloat(a.OfficialAboveHigh, b.OfficialAboveHigh)
}

func closeFloat(a, b float64) bool { return math.Abs(a-b) <= 1e-12 }
