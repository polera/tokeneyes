package tokeneyes

type MediaCapabilities struct {
	InputModalities []string      `json:"input_modalities" yaml:"input_modalities"`
	MIMETypes       []string      `json:"mime_types" yaml:"mime_types"`
	ProcessingModes []string      `json:"processing_modes" yaml:"processing_modes"`
	Endpoint        string        `json:"endpoint" yaml:"endpoint"`
	MaxInlineBytes  int64         `json:"max_inline_bytes" yaml:"max_inline_bytes"`
	MaxAssets       int           `json:"max_assets" yaml:"max_assets"`
	Image           *ImageRule    `json:"image,omitempty" yaml:"image,omitempty"`
	Audio           *AudioRule    `json:"audio,omitempty" yaml:"audio,omitempty"`
	Document        *DocumentRule `json:"document,omitempty" yaml:"document,omitempty"`
}

type ImageRule struct {
	Kind           string                `json:"kind" yaml:"kind"`
	PatchWidth     int                   `json:"patch_width,omitempty" yaml:"patch_width,omitempty"`
	PatchHeight    int                   `json:"patch_height,omitempty" yaml:"patch_height,omitempty"`
	Multiplier     float64               `json:"multiplier,omitempty" yaml:"multiplier,omitempty"`
	SmallThreshold int                   `json:"small_threshold,omitempty" yaml:"small_threshold,omitempty"`
	TileSize       int                   `json:"tile_size,omitempty" yaml:"tile_size,omitempty"`
	TokensPerTile  int64                 `json:"tokens_per_tile,omitempty" yaml:"tokens_per_tile,omitempty"`
	Details        map[string]DetailRule `json:"details" yaml:"details"`
	FormulaVersion string                `json:"formula_version" yaml:"formula_version"`
	Provenance     string                `json:"provenance" yaml:"provenance"`
}

type DetailRule struct {
	Mode        string `json:"mode" yaml:"mode"`
	FixedTokens int64  `json:"fixed_tokens,omitempty" yaml:"fixed_tokens,omitempty"`
	MaxPatches  int64  `json:"max_patches,omitempty" yaml:"max_patches,omitempty"`
	MaxEdge     int    `json:"max_edge,omitempty" yaml:"max_edge,omitempty"`
}

type AudioRule struct {
	TokensPerSecond float64 `json:"tokens_per_second" yaml:"tokens_per_second"`
	FormulaVersion  string  `json:"formula_version" yaml:"formula_version"`
	Provenance      string  `json:"provenance" yaml:"provenance"`
}

type DocumentRule struct {
	PDFMode          string `json:"pdf_mode" yaml:"pdf_mode"`
	RichDocumentMode string `json:"rich_document_mode" yaml:"rich_document_mode"`
	FormulaVersion   string `json:"formula_version" yaml:"formula_version"`
}

func openAIMedia(multiplier float64, originalPatches int64, originalEdge int, autoOriginal bool) MediaCapabilities {
	auto := DetailRule{Mode: "patch", MaxPatches: 2_500, MaxEdge: 2_048}
	if autoOriginal {
		auto = DetailRule{Mode: "patch", MaxPatches: originalPatches, MaxEdge: originalEdge}
	}
	return MediaCapabilities{
		InputModalities: []string{"text", "image", "document"},
		MIMETypes:       []string{"image/png", "image/jpeg", "image/webp", "image/gif", "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		ProcessingModes: []string{"native", "normalized-text"}, Endpoint: "responses", MaxInlineBytes: 50 << 20, MaxAssets: 100,
		Image: &ImageRule{Kind: "patch", PatchWidth: 32, PatchHeight: 32, Multiplier: multiplier, Details: map[string]DetailRule{
			"low":      {Mode: "fixed", FixedTokens: roundedTokens(256 * multiplier), MaxEdge: 512},
			"high":     {Mode: "patch", MaxPatches: 2_500, MaxEdge: 2_048},
			"original": {Mode: "patch", MaxPatches: originalPatches, MaxEdge: originalEdge},
			"auto":     auto,
		}, FormulaVersion: CatalogVersion + ":openai-image-patches", Provenance: "https://developers.openai.com/api/docs/guides/images-vision"},
		Document: &DocumentRule{PDFMode: "text-plus-page-images", RichDocumentMode: "normalized-text", FormulaVersion: CatalogVersion + ":openai-files"},
	}
}

func anthropicMedia(maxEdge int, maxPatches int64) MediaCapabilities {
	return MediaCapabilities{
		InputModalities: []string{"text", "image", "document"},
		MIMETypes:       []string{"image/png", "image/jpeg", "image/webp", "image/gif", "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		ProcessingModes: []string{"native", "normalized-text"}, Endpoint: "messages.count_tokens", MaxInlineBytes: 32 << 20, MaxAssets: 100,
		Image: &ImageRule{Kind: "patch", PatchWidth: 28, PatchHeight: 28, Multiplier: 1, Details: map[string]DetailRule{
			"auto": {Mode: "patch", MaxPatches: maxPatches, MaxEdge: maxEdge}, "low": {Mode: "patch", MaxPatches: 768, MaxEdge: 1_096}, "high": {Mode: "patch", MaxPatches: maxPatches, MaxEdge: maxEdge}, "original": {Mode: "patch", MaxPatches: maxPatches, MaxEdge: maxEdge},
		}, FormulaVersion: CatalogVersion + ":anthropic-28px", Provenance: "https://platform.claude.com/docs/en/build-with-claude/vision"},
		Document: &DocumentRule{PDFMode: "text-plus-page-images", RichDocumentMode: "normalized-text", FormulaVersion: CatalogVersion + ":anthropic-pdf"},
	}
}

func geminiMedia() MediaCapabilities {
	return MediaCapabilities{
		InputModalities: []string{"text", "image", "audio", "document"},
		MIMETypes:       []string{"image/png", "image/jpeg", "image/webp", "image/gif", "audio/wav", "audio/mpeg", "audio/aac", "audio/mp4", "audio/flac", "audio/ogg", "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		ProcessingModes: []string{"native", "normalized-text"}, Endpoint: "models.countTokens", MaxInlineBytes: 20 << 20, MaxAssets: 100,
		Image:    &ImageRule{Kind: "gemini-tile", SmallThreshold: 384, TileSize: 768, TokensPerTile: 258, Details: map[string]DetailRule{"auto": {Mode: "tile"}, "low": {Mode: "fixed", FixedTokens: 258}, "high": {Mode: "tile"}, "original": {Mode: "tile"}}, FormulaVersion: CatalogVersion + ":gemini-768-tiles", Provenance: "https://ai.google.dev/gemini-api/docs/tokens"},
		Audio:    &AudioRule{TokensPerSecond: 32, FormulaVersion: CatalogVersion + ":gemini-audio-32ps", Provenance: "https://ai.google.dev/gemini-api/docs/tokens"},
		Document: &DocumentRule{PDFMode: "text-plus-page-images", RichDocumentMode: "normalized-text", FormulaVersion: CatalogVersion + ":gemini-document"},
	}
}

func (m Model) SupportsModality(want string) bool {
	for _, got := range m.Media.InputModalities {
		if got == want {
			return true
		}
	}
	return want == "text" && len(m.Media.InputModalities) == 0
}
func (m Model) SupportsMIME(want string) bool {
	for _, got := range m.Media.MIMETypes {
		if got == want {
			return true
		}
	}
	return false
}
