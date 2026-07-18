package tokeneyes

type Asset struct {
	ID             string            `json:"id"`
	Label          string            `json:"label"`
	Path           string            `json:"path,omitempty"`
	SHA256         string            `json:"sha256"`
	Bytes          int64             `json:"bytes"`
	DetectedMIME   string            `json:"detected_mime"`
	SourceKind     string            `json:"source_kind"`
	Image          *ImageMetadata    `json:"image,omitempty"`
	Audio          *AudioMetadata    `json:"audio,omitempty"`
	Document       *DocumentMetadata `json:"document,omitempty"`
	TranscriptPath string            `json:"transcript_path,omitempty"`
	Warnings       []string          `json:"warnings,omitempty"`
}

type ImageMetadata struct {
	Width       int `json:"width"`
	Height      int `json:"height"`
	Orientation int `json:"orientation,omitempty"`
	Frames      int `json:"frames"`
}

type AudioMetadata struct {
	DurationMillis int64  `json:"duration_ms"`
	Codec          string `json:"codec"`
	SampleRateHz   int    `json:"sample_rate_hz,omitempty"`
	Channels       int    `json:"channels,omitempty"`
}

type DocumentMetadata struct {
	Format           string         `json:"format"`
	Pages            int            `json:"pages,omitempty"`
	Encrypted        bool           `json:"encrypted"`
	ExtractionStatus string         `json:"extraction_status"`
	ExtractedBytes   int64          `json:"extracted_bytes,omitempty"`
	PageMetadata     []PageMetadata `json:"page_metadata,omitempty"`
}

type PageMetadata struct {
	Number         int     `json:"number"`
	WidthPoints    float64 `json:"width_points,omitempty"`
	HeightPoints   float64 `json:"height_points,omitempty"`
	Classification string  `json:"classification"`
}

type PlannedPart struct {
	AssetID         string   `json:"asset_id,omitempty"`
	Source          string   `json:"source"`
	ProviderType    string   `json:"provider_part_type"`
	Processing      string   `json:"processing_mode"`
	Detail          string   `json:"detail,omitempty"`
	Transformations []string `json:"transformations,omitempty"`
}

type CountComponent struct {
	AssetID              string   `json:"asset_id,omitempty"`
	Source               string   `json:"source"`
	Modality             string   `json:"modality"`
	Unit                 string   `json:"unit"`
	Low                  int64    `json:"low"`
	Expected             int64    `json:"expected"`
	High                 int64    `json:"high"`
	Method               string   `json:"method"`
	Confidence           float64  `json:"confidence"`
	FormulaVersion       string   `json:"formula_version"`
	Processing           string   `json:"processing_mode"`
	Warnings             []string `json:"warnings,omitempty"`
	InputMicrosUSD       int64    `json:"input_micros_usd,omitempty"`
	CachedInputMicrosUSD int64    `json:"cached_input_micros_usd,omitempty"`
	Detail               string   `json:"detail,omitempty"`
	OriginalWidth        int      `json:"original_width,omitempty"`
	OriginalHeight       int      `json:"original_height,omitempty"`
	PlannedWidth         int      `json:"planned_width,omitempty"`
	PlannedHeight        int      `json:"planned_height,omitempty"`
	PatchCount           int64    `json:"patch_count,omitempty"`
	TileCount            int64    `json:"tile_count,omitempty"`
	Multiplier           float64  `json:"multiplier,omitempty"`
}
