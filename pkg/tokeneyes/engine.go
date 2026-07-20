package tokeneyes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Engine struct {
	Catalog  Catalog
	Counter  TokenCounter
	Verifier ProviderVerifier
	Now      func() time.Time
}

func NewEngine(c Catalog) *Engine {
	return &Engine{Catalog: c, Counter: NewLocalCounter(), Verifier: NewHTTPVerifier(), Now: time.Now}
}

func (e *Engine) Analyze(ctx context.Context, req AnalyzeRequest) (Run, error) {
	if e.Counter == nil {
		return Run{}, fmt.Errorf("token counter is required")
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if req.Processing == "" {
		req.Processing = "native"
	}
	if req.ImageDetail == "" {
		req.ImageDetail = "auto"
	}
	if req.DocumentDetail == "" {
		req.DocumentDetail = "auto"
	}
	if req.EstimateBound == "" {
		req.EstimateBound = "expected"
	}
	if req.Processing != "native" && req.Processing != "normalized-text" {
		return Run{}, fmt.Errorf("unknown processing mode %q", req.Processing)
	}
	if !containsString([]string{"auto", "low", "high", "original"}, req.ImageDetail) {
		return Run{}, fmt.Errorf("unknown image detail %q", req.ImageDetail)
	}
	if !containsString([]string{"auto", "text", "low", "high"}, req.DocumentDetail) {
		return Run{}, fmt.Errorf("unknown document detail %q", req.DocumentDetail)
	}
	if !containsString([]string{"expected", "high"}, req.EstimateBound) {
		return Run{}, fmt.Errorf("unknown estimate bound %q", req.EstimateBound)
	}
	if req.AllowFileUpload && !req.Verify {
		return Run{}, fmt.Errorf("file upload authorization requires verification")
	}
	if len(req.Models) == 0 {
		req.Models = []string{"gpt-5.5"}
	}
	if len(req.OutputTokens) == 0 {
		req.OutputTokens = []int64{1_000, 4_000, 16_000}
	}
	for _, n := range req.OutputTokens {
		if n < 0 {
			return Run{}, fmt.Errorf("output tokens cannot be negative")
		}
	}
	if req.ReasoningTokens < 0 || req.CachedTokens < 0 {
		return Run{}, fmt.Errorf("reasoning and cached tokens cannot be negative")
	}
	workers := boundedWorkers(req.Workers)

	models := make([]Model, len(req.Models))
	for i, name := range req.Models {
		m, err := e.Catalog.Resolve(name)
		if err != nil {
			return Run{}, err
		}
		models[i] = m
	}
	rawContent := assembleRaw(req.Collection.Sources)
	assembled := assemble(req.Collection.Sources)
	results := make([]ModelResult, len(models))
	errs := make([]error, len(models))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, model := range models {
		wg.Add(1)
		go func(i int, model Model) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			results[i], errs[i] = e.analyzeModel(ctx, model, rawContent, assembled, req)
		}(i, model)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return Run{}, err
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Model < results[j].Model })

	now := e.Now().UTC()
	run := Run{SchemaVersion: SchemaVersion, ID: newRunID(now), CreatedAt: now, CatalogVersion: e.Catalog.Version, Results: results, Warnings: append([]string(nil), req.Collection.Warnings...), Incomplete: req.Collection.Incomplete}
	run.Config = RunConfig{Models: modelIDs(models), OutputTokens: append([]int64(nil), req.OutputTokens...), ReasoningTokens: req.ReasoningTokens, CachedTokens: req.CachedTokens, Preset: req.Preset, Verified: req.Verify, Processing: req.Processing, ImageDetail: req.ImageDetail, DocumentDetail: req.DocumentDetail, AllowFileUpload: req.AllowFileUpload, EstimateBound: req.EstimateBound}
	run.Sources = privacySources(req.Collection.Sources)
	run.Assets = append([]Asset(nil), req.Collection.Assets...)
	for _, result := range results {
		if result.CapabilityStatus != "supported" {
			run.Incomplete = true
		}
	}
	return run, nil
}
func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func (e *Engine) analyzeModel(ctx context.Context, model Model, rawContent, assembled string, req AnalyzeRequest) (ModelResult, error) {
	input, err := e.Counter.Count(model, []byte(rawContent))
	if err != nil {
		return ModelResult{}, err
	}
	shaped, err := e.Counter.Count(model, []byte(assembled))
	if err != nil {
		return ModelResult{}, err
	}
	system, err := e.Counter.Count(model, []byte(req.System))
	if err != nil {
		return ModelResult{}, err
	}
	tools, err := e.Counter.Count(model, []byte(req.Tools))
	if err != nil {
		return ModelResult{}, err
	}
	wrapperTokens := shaped.Tokens - input.Tokens
	if wrapperTokens < 0 {
		wrapperTokens = 0
	}
	wrapperLow := shaped.Low - input.High
	if wrapperLow < 0 {
		wrapperLow = 0
	}
	wrapperHigh := shaped.High - input.Low
	if wrapperHigh < 0 {
		wrapperHigh = 0
	}
	overhead := Overhead{SystemTokens: system.Tokens, ToolTokens: tools.Tokens, WrapperTokens: wrapperTokens}
	exactProfileOverhead := int64(0)
	switch req.Profile {
	case "", "none":
	case "chat":
		overhead.WrapperTokens += 7
		exactProfileOverhead += 7
	case "codex":
		overhead.WrapperTokens += 12
		overhead.RuntimeTokens = 1_200
		exactProfileOverhead += 1_212
	default:
		return ModelResult{}, fmt.Errorf("unknown overhead profile %q", req.Profile)
	}
	planned, err := planMedia(model, req.Collection.Sources, e.Counter, req.Processing, req.ImageDetail, req.DocumentDetail, req.Overrides...)
	if err != nil {
		return ModelResult{}, err
	}
	componentTotal, componentLow, componentHigh := int64(0), int64(0), int64(0)
	for _, c := range planned.Components {
		if c.Unit == "tokens" {
			componentTotal += c.Expected
			componentLow += c.Low
			componentHigh += c.High
		}
	}
	hasMedia := false
	for _, s := range req.Collection.Sources {
		if s.Kind == "image" || s.Kind == "audio" || s.Kind == "document" {
			hasMedia = true
			break
		}
	}
	if !hasMedia {
		componentTotal, componentLow, componentHigh = input.Tokens, input.Low, input.High
	}
	total := componentTotal + overhead.Total()
	overheadLow := system.Low + tools.Low + wrapperLow + exactProfileOverhead
	overheadHigh := system.High + tools.High + wrapperHigh + exactProfileOverhead
	low, high := componentLow+overheadLow, componentHigh+overheadHigh
	sources := planned.Sources
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].Count.Tokens > sources[j].Count.Tokens })
	confidence := input.Confidence
	if hasMedia {
		confidence = 0
		set := false
		for _, c := range planned.Components {
			if c.Unit == "tokens" && (!set || c.Confidence < confidence) {
				confidence = c.Confidence
				set = true
			}
		}
	}
	decisionTotal := total
	if req.EstimateBound == "high" {
		decisionTotal = high
	}
	r := ModelResult{Model: model.ID, Provider: model.Provider, InputTokens: total, InputLow: low, InputHigh: high, EstimateBound: req.EstimateBound, DecisionInputTokens: decisionTotal, CachedInputTokens: min64(req.CachedTokens, total), Overhead: overhead, ContextWindow: model.ContextWindow, ContextUtilization: float64(total) / float64(model.ContextWindow), CounterMethod: "modality component aggregate", FormulaVersion: input.FormulaVersion, Confidence: confidence, Sources: sources, RequestPlan: planned.Plan, CountComponents: planned.Components, CapabilityStatus: planned.Status, PricingDate: model.PricingDate, Warnings: append([]string(nil), planned.Warnings...)}
	if req.Profile == "" || req.Profile == "none" {
		r.Warnings = append(r.Warnings, "system, tool, and agent-runtime overhead default to zero unless explicitly supplied")
	}
	if model.PricingStale(e.Now()) {
		r.Warnings = append(r.Warnings, "catalog pricing is stale or past its validity window")
	}
	r.Verification.Requested = req.Verify
	if req.Verify {
		r.Verification.Provider = model.Provider
		if planned.Status == "unsupported" {
			verifyErr := fmt.Errorf("planned request contains unsupported media and cannot be sent as a partial verification")
			r.Verification.Error = verifyErr.Error()
			r.Warnings = append(r.Warnings, "verification failed: "+verifyErr.Error())
			if req.RequireVerify {
				return ModelResult{}, verifyErr
			}
		} else if e.Verifier == nil {
			return ModelResult{}, fmt.Errorf("verification requested but no verifier is configured")
		} else {
			verified, verifyErr := e.Verifier.Verify(ctx, model, AssembledRequest{System: req.System, Tools: req.Tools, Content: assembled, Parts: planned.Parts, AllowFileUpload: req.AllowFileUpload})
			r.Verification.Tokens, r.Verification.Method = verified.Tokens, verified.Method
			r.Verification.Transport, r.Verification.CleanupStatus = verified.Transport, verified.CleanupStatus
			if verifyErr != nil {
				r.Verification.Error = verifyErr.Error()
				r.Warnings = append(r.Warnings, "verification failed: "+verifyErr.Error())
				if req.RequireVerify {
					return ModelResult{}, verifyErr
				}
			} else {
				r.DecisionInputTokens = verified.Tokens
				if verified.Tokens != total {
					r.Warnings = append(r.Warnings, fmt.Sprintf("official aggregate differs from local estimate by %+d tokens", verified.Tokens-total))
				}
			}
		}
	}
	if r.DecisionInputTokens > model.ContextWindow {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%s input exceeds context window by %d tokens", decisionMethod(r), r.DecisionInputTokens-model.ContextWindow))
	}
	r.CachedInputTokens = min64(req.CachedTokens, r.DecisionInputTokens)
	componentRegular, componentCached := int64(0), int64(0)
	if hasMedia && !verifiedDecision(r) {
		pricingOverhead := overhead.Total()
		if req.EstimateBound == "high" {
			pricingOverhead = overheadHigh
		}
		r.CountComponents, componentRegular, componentCached = PriceComponentsBound(model, r.CountComponents, pricingOverhead, r.CachedInputTokens, r.DecisionInputTokens, req.EstimateBound)
	}
	for i, output := range req.OutputTokens {
		name := scenarioName(i, len(req.OutputTokens))
		breakdown := ScenarioCostBreakdown(model, r.DecisionInputTokens, r.CachedInputTokens, output, req.ReasoningTokens)
		if hasMedia && !verifiedDecision(r) {
			tier := model.Price(r.DecisionInputTokens)
			breakdown.InputMicrosUSD = componentRegular
			breakdown.CachedInputMicrosUSD = componentCached
			breakdown.OutputMicrosUSD = CostMicros(tier.OutputMicrosPerMTok, output)
			breakdown.ReasoningMicrosUSD = CostMicros(tier.OutputMicrosPerMTok, req.ReasoningTokens)
		}
		cost := breakdown.Total()
		r.Scenarios = append(r.Scenarios, OutputScenario{Name: name, OutputTokens: output, ReasoningTokens: req.ReasoningTokens, CostBreakdown: breakdown, CostMicrosUSD: cost, CostUSD: FormatUSD(cost)})
		if output+req.ReasoningTokens > model.MaxOutput {
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s scenario exceeds max output by %d tokens", name, output+req.ReasoningTokens-model.MaxOutput))
		}
	}
	return r, nil
}

func decisionMethod(r ModelResult) string {
	if verifiedDecision(r) {
		return "officially verified"
	}
	return r.EstimateBound + "-bound"
}

func verifiedDecision(r ModelResult) bool {
	return r.Verification.Method != "" && r.Verification.Error == ""
}

func assemble(sources []Source) string {
	var b strings.Builder
	for _, s := range sources {
		content := countableText(s)
		if len(content) == 0 {
			continue
		}
		b.WriteString("<source label=\"")
		b.WriteString(strings.ReplaceAll(s.Label, "\"", "'"))
		b.WriteString("\">\n")
		b.Write(content)
		if len(content) > 0 && content[len(content)-1] != '\n' {
			b.WriteByte('\n')
		}
		b.WriteString("</source>\n")
	}
	return b.String()
}

func assembleRaw(sources []Source) string {
	var b strings.Builder
	for i, s := range sources {
		content := countableText(s)
		if len(content) == 0 {
			continue
		}
		if i > 0 && b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.Write(content)
	}
	return b.String()
}

func privacySources(in []Source) []Source {
	out := make([]Source, len(in))
	for i, s := range in {
		out[i] = Source{Label: s.Label, Path: s.Path, Kind: s.Kind, SHA256: s.SHA256, Bytes: s.Bytes, AssetID: s.AssetID, DetectedMIME: s.DetectedMIME}
	}
	return out
}
func countableText(s Source) []byte {
	if s.Kind == "image" {
		return nil
	}
	if s.Kind == "audio" || s.Kind == "document" {
		return s.ExtractedText
	}
	return s.Content
}

func modelIDs(models []Model) []string {
	out := make([]string, len(models))
	for i := range models {
		out[i] = models[i].ID
	}
	return out
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func scenarioName(i, total int) string {
	if total == 1 {
		return "expected"
	}
	if total == 3 {
		return []string{"low", "expected", "high"}[i]
	}
	return fmt.Sprintf("scenario-%d", i+1)
}

func newRunID(t time.Time) string {
	var random [4]byte
	_, _ = rand.Read(random[:])
	return t.Format("20060102T150405Z") + "-" + hex.EncodeToString(random[:])
}
