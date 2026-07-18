package tokeneyes

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

type mediaPlan struct {
	Components []CountComponent
	Parts      []RequestPart
	Plan       []PlannedPart
	Sources    []SourceResult
	Status     string
	Warnings   []string
}

func planMedia(model Model, sources []Source, counter TokenCounter, processing, imageDetail, documentDetail string, overrides ...ProcessingOverride) (mediaPlan, error) {
	if processing == "" {
		processing = "native"
	}
	if imageDetail == "" {
		imageDetail = "auto"
	}
	if documentDetail == "" {
		documentDetail = "auto"
	}
	out := mediaPlan{Status: "supported"}
	mediaAssets := 0
	for _, s := range sources {
		if s.AssetID != "" {
			mediaAssets++
		}
	}
	if model.Media.MaxAssets > 0 && mediaAssets > model.Media.MaxAssets {
		out.Status = "unsupported"
		out.Warnings = append(out.Warnings, fmt.Sprintf("request has %d media assets; model limit is %d", mediaAssets, model.Media.MaxAssets))
	}
	for _, s := range sources {
		sourceProcessing, sourceImageDetail, sourceDocumentDetail := processing, imageDetail, documentDetail
		for _, rule := range overrides {
			matched, err := matchOverrideGlob(rule.Glob, s.Path)
			if err != nil {
				return out, fmt.Errorf("invalid processing override glob %q: %w", rule.Glob, err)
			}
			if matched {
				if rule.Processing != "" {
					sourceProcessing = rule.Processing
				}
				if rule.ImageDetail != "" {
					sourceImageDetail = rule.ImageDetail
				}
				if rule.DocumentDetail != "" {
					sourceDocumentDetail = rule.DocumentDetail
				}
			}
		}
		before := len(out.Components)
		switch s.Kind {
		case "image":
			if !model.SupportsMIME(s.DetectedMIME) {
				markUnsupported(&out, s, "model does not accept "+s.DetectedMIME)
				break
			}
			if sourceProcessing == "normalized-text" {
				markUnsupported(&out, s, "image normalized-text requires user-supplied OCR or description")
				break
			}
			if !model.SupportsModality("image") || model.Media.Image == nil {
				markUnsupported(&out, s, "model does not support image input")
				break
			}
			m, err := imageMetadataFromSource(s)
			if err != nil {
				return out, err
			}
			c, transforms, err := countImageRule(*model.Media.Image, m.Width, m.Height, sourceImageDetail)
			if err != nil {
				return out, fmt.Errorf("%s: %w", s.Label, err)
			}
			c.AssetID, c.Source, c.Processing = s.AssetID, s.Label, sourceProcessing
			out.Components = append(out.Components, c)
			out.Parts = append(out.Parts, RequestPart{AssetID: s.AssetID, Type: "image", MIME: s.DetectedMIME, Detail: sourceImageDetail, Data: s.Content})
			out.Plan = append(out.Plan, PlannedPart{AssetID: s.AssetID, Source: s.Label, ProviderType: "image", Processing: sourceProcessing, Detail: sourceImageDetail, Transformations: transforms})
		case "audio":
			if !model.SupportsMIME(s.DetectedMIME) && sourceProcessing == "native" {
				markUnsupported(&out, s, "model does not accept "+s.DetectedMIME)
				break
			}
			if sourceProcessing == "normalized-text" {
				if len(s.ExtractedText) == 0 {
					markUnsupported(&out, s, "normalized audio requires --transcript")
					break
				}
			} else if !model.SupportsModality("audio") || model.Media.Audio == nil {
				markUnsupported(&out, s, "model does not support native audio input")
			} else {
				m, err := audioMetadataFromSource(s)
				if err != nil {
					return out, err
				}
				tokens := roundedTokens(float64(m.DurationMillis) / 1000 * model.Media.Audio.TokensPerSecond)
				c := CountComponent{AssetID: s.AssetID, Source: s.Label, Modality: "audio", Unit: "tokens", Low: tokens, Expected: tokens, High: tokens, Method: "published duration formula", Confidence: 1, FormulaVersion: model.Media.Audio.FormulaVersion, Processing: sourceProcessing}
				out.Components = append(out.Components, c)
				out.Parts = append(out.Parts, RequestPart{AssetID: s.AssetID, Type: "audio", MIME: s.DetectedMIME, Data: s.Content})
				out.Plan = append(out.Plan, PlannedPart{AssetID: s.AssetID, Source: s.Label, ProviderType: "audio", Processing: sourceProcessing})
			}
			if len(s.ExtractedText) > 0 {
				appendTextComponent(&out, model, counter, s, s.ExtractedText, "transcript")
				out.Parts = append(out.Parts, RequestPart{AssetID: s.AssetID, Type: "text", Text: string(s.ExtractedText)})
				out.Plan = append(out.Plan, PlannedPart{AssetID: s.AssetID, Source: s.Label + " transcript", ProviderType: "text", Processing: "normalized-text", Transformations: []string{"user-supplied transcript sidecar"}})
			}
		case "document":
			if !model.SupportsMIME(s.DetectedMIME) && sourceProcessing == "native" {
				markUnsupported(&out, s, "model does not accept "+s.DetectedMIME)
				break
			}
			m, err := documentMetadataFromSource(s)
			if err != nil {
				return out, err
			}
			if m.Format != "pdf" || sourceProcessing == "normalized-text" {
				if len(s.ExtractedText) == 0 {
					markUnsupported(&out, s, "document text extraction unavailable")
					break
				}
				appendTextComponent(&out, model, counter, s, s.ExtractedText, "normalized document text")
				if m.Format != "pdf" && sourceProcessing == "native" {
					out.Status = maxStatus(out.Status, "conditional")
					out.Warnings = append(out.Warnings, s.Label+": provider rich-document transformation is modeled as extracted text; embedded images/charts are ignored")
				}
				out.Plan = append(out.Plan, PlannedPart{AssetID: s.AssetID, Source: s.Label, ProviderType: "text", Processing: "normalized-text", Transformations: []string{"deterministic text extraction"}})
				out.Parts = append(out.Parts, RequestPart{AssetID: s.AssetID, Type: "text", Text: string(s.ExtractedText)})
				break
			}
			if !model.SupportsModality("document") || model.Media.Document == nil {
				markUnsupported(&out, s, "model does not support native PDF input")
				break
			}
			if len(s.ExtractedText) > 0 {
				appendTextComponent(&out, model, counter, s, s.ExtractedText, "PDF extracted text")
			} else {
				if sourceDocumentDetail == "text" {
					markUnsupported(&out, s, "text-only PDF processing requested but embedded text extraction is unavailable")
					break
				}
				out.Status = maxStatus(out.Status, "conditional")
				out.Warnings = append(out.Warnings, s.Label+": embedded text extraction was unavailable")
			}
			if sourceDocumentDetail != "text" {
				if model.Media.Image == nil {
					return out, fmt.Errorf("catalog_rule_missing: PDF page-image rule")
				}
				for i := 0; i < m.Pages; i++ {
					w, h := 1275, 1650
					if i < len(m.PageMetadata) && m.PageMetadata[i].WidthPoints > 0 {
						w = int(math.Ceil(m.PageMetadata[i].WidthPoints * 150 / 72))
						h = int(math.Ceil(m.PageMetadata[i].HeightPoints * 150 / 72))
					}
					c, _, e := countImageRule(*model.Media.Image, w, h, mapDocumentDetail(sourceDocumentDetail))
					if e != nil {
						return out, e
					}
					c.AssetID = s.AssetID
					c.Source = fmt.Sprintf("%s page %d", s.Label, i+1)
					c.Processing = sourceProcessing
					c.FormulaVersion = model.Media.Document.FormulaVersion + "+" + c.FormulaVersion
					if i < len(m.PageMetadata) && m.PageMetadata[i].Classification == "unknown" {
						c.Low = 0
						c.Confidence = .65
						c.Warnings = append(c.Warnings, "page classification unknown")
					}
					out.Components = append(out.Components, c)
				}
			}
			if sourceDocumentDetail == "text" {
				out.Parts = append(out.Parts, RequestPart{AssetID: s.AssetID, Type: "text", Text: string(s.ExtractedText)})
				out.Plan = append(out.Plan, PlannedPart{AssetID: s.AssetID, Source: s.Label, ProviderType: "text", Processing: "normalized-text", Detail: "text", Transformations: []string{"local embedded-text extraction", "page vision excluded"}})
			} else {
				out.Parts = append(out.Parts, RequestPart{AssetID: s.AssetID, Type: "document", MIME: s.DetectedMIME, Detail: sourceDocumentDetail, Data: s.Content})
				out.Plan = append(out.Plan, PlannedPart{AssetID: s.AssetID, Source: s.Label, ProviderType: "document", Processing: sourceProcessing, Detail: sourceDocumentDetail, Transformations: []string{"provider extracts text", "provider renders each PDF page"}})
			}
		default:
			appendTextComponent(&out, model, counter, s, s.Content, "local tokenizer")
			out.Parts = append(out.Parts, RequestPart{Type: "text", Text: string(s.Content)})
			out.Plan = append(out.Plan, PlannedPart{Source: s.Label, ProviderType: "text", Processing: "normalized-text"})
		}
		var sr Count
		for _, c := range out.Components[before:] {
			if c.Unit == "tokens" {
				sr.Tokens += c.Expected
				sr.Low += c.Low
				sr.High += c.High
				if c.Confidence > 0 && (sr.Confidence == 0 || c.Confidence < sr.Confidence) {
					sr.Confidence = c.Confidence
				}
			}
		}
		sr.Method = "component aggregate"
		out.Sources = append(out.Sources, SourceResult{Label: s.Label, Path: s.Path, Kind: s.Kind, SHA256: s.SHA256, Bytes: s.Bytes, Count: sr})
	}
	return out, nil
}

func appendTextComponent(out *mediaPlan, model Model, counter TokenCounter, s Source, b []byte, method string) {
	c, err := counter.Count(model, b)
	if err != nil {
		out.Status = maxStatus(out.Status, "unknown")
		out.Warnings = append(out.Warnings, s.Label+": "+err.Error())
		return
	}
	out.Components = append(out.Components, CountComponent{AssetID: s.AssetID, Source: s.Label, Modality: "text", Unit: "tokens", Low: c.Low, Expected: c.Tokens, High: c.High, Method: method + " / " + c.Method, Confidence: c.Confidence, FormulaVersion: CatalogVersion + ":" + model.Tokenizer, Processing: "normalized-text"})
}
func markUnsupported(out *mediaPlan, s Source, msg string) {
	out.Status = "unsupported"
	out.Warnings = append(out.Warnings, s.Label+": "+msg)
	out.Plan = append(out.Plan, PlannedPart{AssetID: s.AssetID, Source: s.Label, ProviderType: "unsupported", Processing: "unsupported"})
}
func maxStatus(a, b string) string {
	rank := map[string]int{"supported": 0, "conditional": 1, "unknown": 2, "unsupported": 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func countImageRule(rule ImageRule, w, h int, detail string) (CountComponent, []string, error) {
	d, ok := rule.Details[detail]
	if !ok {
		return CountComponent{}, nil, fmt.Errorf("image detail %q is unsupported by catalog rule", detail)
	}
	c := CountComponent{Modality: "image", Unit: "tokens", Method: "published image formula", Confidence: 1, FormulaVersion: rule.FormulaVersion, Detail: detail, OriginalWidth: w, OriginalHeight: h, PlannedWidth: w, PlannedHeight: h, Multiplier: rule.Multiplier}
	if d.Mode == "fixed" {
		if d.MaxEdge > 0 {
			c.PlannedWidth, c.PlannedHeight = d.MaxEdge, d.MaxEdge
			if rule.PatchWidth > 0 && rule.PatchHeight > 0 {
				c.PatchCount = int64(ceilDiv(d.MaxEdge, rule.PatchWidth) * ceilDiv(d.MaxEdge, rule.PatchHeight))
			}
		}
		c.Low = d.FixedTokens
		c.Expected = d.FixedTokens
		c.High = d.FixedTokens
		return c, []string{fmt.Sprintf("fixed %d tokens", d.FixedTokens)}, nil
	}
	ow, oh := w, h
	if rule.Kind == "gemini-tile" {
		var tiles int
		if w <= rule.SmallThreshold && h <= rule.SmallThreshold {
			tiles = 1
		} else {
			tiles = ceilDiv(w, rule.TileSize) * ceilDiv(h, rule.TileSize)
		}
		t := int64(tiles) * rule.TokensPerTile
		c.TileCount = int64(tiles)
		c.Low = t
		c.Expected = t
		c.High = t
		return c, []string{fmt.Sprintf("%dx%d -> %d tile(s)", w, h, tiles)}, nil
	}
	if rule.Kind != "patch" || rule.PatchWidth <= 0 || rule.PatchHeight <= 0 {
		return c, nil, fmt.Errorf("catalog_rule_missing: invalid image rule")
	}
	if d.MaxEdge > 0 && (w > d.MaxEdge || h > d.MaxEdge) {
		scale := math.Min(float64(d.MaxEdge)/float64(w), float64(d.MaxEdge)/float64(h))
		w = maxInt(1, int(math.Floor(float64(w)*scale)))
		h = maxInt(1, int(math.Floor(float64(h)*scale)))
	}
	patches := int64(ceilDiv(w, rule.PatchWidth) * ceilDiv(h, rule.PatchHeight))
	if d.MaxPatches > 0 && patches > d.MaxPatches {
		scale := math.Sqrt(float64(d.MaxPatches) / float64(patches))
		w = maxInt(1, int(math.Floor(float64(w)*scale)))
		h = maxInt(1, int(math.Floor(float64(h)*scale)))
		for int64(ceilDiv(w, rule.PatchWidth)*ceilDiv(h, rule.PatchHeight)) > d.MaxPatches {
			if w >= h {
				w--
			} else {
				h--
			}
		}
		patches = int64(ceilDiv(w, rule.PatchWidth) * ceilDiv(h, rule.PatchHeight))
	}
	tokens := roundedTokens(float64(patches) * rule.Multiplier)
	c.PlannedWidth, c.PlannedHeight, c.PatchCount = w, h, patches
	c.Low = tokens
	c.Expected = tokens
	c.High = tokens
	return c, []string{fmt.Sprintf("%dx%d -> %dx%d, %d patches x %.2f", ow, oh, w, h, patches, rule.Multiplier)}, nil
}

func mapDocumentDetail(d string) string {
	if d == "text" {
		return "low"
	}
	if d == "" || d == "auto" {
		return "auto"
	}
	return d
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Metadata is held in Collection.Assets; sources deliberately retain only the
// minimum in-memory link. These decoders repeat bounded header inspection so a
// planner can also be used independently in tests.
func imageMetadataFromSource(s Source) (ImageMetadata, error) {
	return inspectImage(s.DetectedMIME, s.Content)
}
func audioMetadataFromSource(s Source) (AudioMetadata, error) {
	return inspectAudio(s.DetectedMIME, s.Content)
}
func documentMetadataFromSource(s Source) (DocumentMetadata, error) {
	if s.DetectedMIME == "application/pdf" {
		m, _, e := inspectPDF(s.Content)
		return m, e
	}
	format := "docx"
	if strings.Contains(s.DetectedMIME, "presentation") {
		format = "pptx"
	}
	if strings.Contains(s.DetectedMIME, "spreadsheet") {
		format = "xlsx"
	}
	return DocumentMetadata{Format: format, ExtractionStatus: "complete"}, nil
}
func matchOverrideGlob(pattern, name string) (bool, error) {
	if pattern == "" {
		return false, fmt.Errorf("glob is empty")
	}
	q := regexp.QuoteMeta(strings.ReplaceAll(pattern, "\\", "/"))
	q = strings.ReplaceAll(q, `\*\*/`, `(?:.*/)?`)
	q = strings.ReplaceAll(q, `\*\*`, `.*`)
	q = strings.ReplaceAll(q, `\*`, `[^/]*`)
	q = strings.ReplaceAll(q, `\?`, `[^/]`)
	re, err := regexp.Compile("^" + q + "$")
	if err != nil {
		return false, err
	}
	return re.MatchString(strings.ReplaceAll(name, "\\", "/")), nil
}
