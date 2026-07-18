package tokeneyes

import (
	"context"
	"io"
	"time"
)

const SchemaVersion = "tokeneyes.run.v2"

// Source contains normalized content only while an analysis is in memory. Content
// is deliberately excluded from JSON and is never passed to RunStore.
type Source struct {
	Label         string `json:"label"`
	Path          string `json:"path,omitempty"`
	Kind          string `json:"kind"`
	SHA256        string `json:"sha256"`
	Bytes         int64  `json:"bytes"`
	AssetID       string `json:"asset_id,omitempty"`
	DetectedMIME  string `json:"detected_mime,omitempty"`
	Content       []byte `json:"-"`
	ExtractedText []byte `json:"-"`
}

type Collection struct {
	Sources    []Source `json:"sources"`
	Assets     []Asset  `json:"assets,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Incomplete bool     `json:"incomplete"`
	TotalBytes int64    `json:"total_bytes"`
}

type CollectRequest struct {
	Paths         []string
	Prompt        string
	PromptFile    string
	Preset        string
	Root          string
	Stdin         io.Reader
	ReadStdin     bool
	MaxFileBytes  int64
	MaxTotalBytes int64
	IncludeHidden bool
	MaxMediaBytes int64
	MaxMediaCount int
	MaxPages      int
	MaxDuration   time.Duration
	Transcripts   []string
}

type Count struct {
	Tokens     int64   `json:"tokens"`
	Low        int64   `json:"low"`
	High       int64   `json:"high"`
	Method     string  `json:"method"`
	Confidence float64 `json:"confidence"`
}

type SourceResult struct {
	Label  string `json:"label"`
	Path   string `json:"path,omitempty"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Count  Count  `json:"count"`
}

type OutputScenario struct {
	Name            string        `json:"name"`
	OutputTokens    int64         `json:"output_tokens"`
	ReasoningTokens int64         `json:"reasoning_tokens"`
	CostBreakdown   CostBreakdown `json:"cost_breakdown"`
	CostMicrosUSD   int64         `json:"cost_micros_usd"`
	CostUSD         string        `json:"cost_usd"`
}

type CostBreakdown struct {
	InputMicrosUSD       int64 `json:"input_micros_usd"`
	CachedInputMicrosUSD int64 `json:"cached_input_micros_usd"`
	OutputMicrosUSD      int64 `json:"output_micros_usd"`
	ReasoningMicrosUSD   int64 `json:"reasoning_micros_usd"`
}

func (c CostBreakdown) Total() int64 {
	return c.InputMicrosUSD + c.CachedInputMicrosUSD + c.OutputMicrosUSD + c.ReasoningMicrosUSD
}

type Overhead struct {
	SystemTokens  int64 `json:"system_tokens"`
	ToolTokens    int64 `json:"tool_tokens"`
	WrapperTokens int64 `json:"wrapper_tokens"`
	RuntimeTokens int64 `json:"runtime_tokens"`
}

func (o Overhead) Total() int64 {
	return o.SystemTokens + o.ToolTokens + o.WrapperTokens + o.RuntimeTokens
}

type Verification struct {
	Requested     bool   `json:"requested"`
	Provider      string `json:"provider,omitempty"`
	Method        string `json:"method,omitempty"`
	Transport     string `json:"transport,omitempty"`
	Tokens        int64  `json:"tokens,omitempty"`
	CleanupStatus string `json:"cleanup_status,omitempty"`
	Error         string `json:"error,omitempty"`
}

type ModelResult struct {
	Model              string           `json:"model"`
	Provider           string           `json:"provider"`
	InputTokens        int64            `json:"input_tokens"`
	InputLow           int64            `json:"input_low"`
	InputHigh          int64            `json:"input_high"`
	CachedInputTokens  int64            `json:"cached_input_tokens"`
	Overhead           Overhead         `json:"overhead"`
	ContextWindow      int64            `json:"context_window"`
	ContextUtilization float64          `json:"context_utilization"`
	CounterMethod      string           `json:"counter_method"`
	Confidence         float64          `json:"confidence"`
	Sources            []SourceResult   `json:"sources"`
	RequestPlan        []PlannedPart    `json:"request_plan,omitempty"`
	CountComponents    []CountComponent `json:"count_components,omitempty"`
	CapabilityStatus   string           `json:"capability_status"`
	Scenarios          []OutputScenario `json:"scenarios"`
	Verification       Verification     `json:"verification"`
	PricingDate        string           `json:"pricing_date"`
	Warnings           []string         `json:"warnings,omitempty"`
}

type Run struct {
	SchemaVersion  string        `json:"schema_version"`
	ID             string        `json:"id"`
	CreatedAt      time.Time     `json:"created_at"`
	CatalogVersion string        `json:"catalog_version"`
	Config         RunConfig     `json:"config"`
	Sources        []Source      `json:"sources"`
	Assets         []Asset       `json:"assets,omitempty"`
	Results        []ModelResult `json:"results"`
	Warnings       []string      `json:"warnings,omitempty"`
	Incomplete     bool          `json:"incomplete"`
}

type RunConfig struct {
	Models          []string `json:"models"`
	OutputTokens    []int64  `json:"output_tokens"`
	ReasoningTokens int64    `json:"reasoning_tokens"`
	CachedTokens    int64    `json:"cached_tokens"`
	Preset          string   `json:"preset,omitempty"`
	Verified        bool     `json:"verified"`
	Processing      string   `json:"processing,omitempty"`
	ImageDetail     string   `json:"image_detail,omitempty"`
	DocumentDetail  string   `json:"document_detail,omitempty"`
	AllowFileUpload bool     `json:"allow_file_upload,omitempty"`
}

type AnalyzeRequest struct {
	Collection      Collection
	Models          []string
	OutputTokens    []int64
	ReasoningTokens int64
	CachedTokens    int64
	System          string
	Tools           string
	Profile         string
	Verify          bool
	RequireVerify   bool
	Workers         int
	Preset          string
	Processing      string
	ImageDetail     string
	DocumentDetail  string
	AllowFileUpload bool
	Overrides       []ProcessingOverride
}

type ProcessingOverride struct {
	Glob           string `json:"glob" yaml:"glob"`
	Processing     string `json:"processing,omitempty" yaml:"processing,omitempty"`
	ImageDetail    string `json:"image_detail,omitempty" yaml:"image_detail,omitempty"`
	DocumentDetail string `json:"document_detail,omitempty" yaml:"document_detail,omitempty"`
}

type RunSummary struct {
	ID             string    `json:"id"`
	CreatedAt      time.Time `json:"created_at"`
	CatalogVersion string    `json:"catalog_version"`
	Models         []string  `json:"models"`
	TotalBytes     int64     `json:"total_bytes"`
	Incomplete     bool      `json:"incomplete"`
}

type RunDiff struct {
	RunA    string      `json:"run_a"`
	RunB    string      `json:"run_b"`
	Models  []ModelDiff `json:"models"`
	Sources SourceDiff  `json:"sources"`
}

type ModelDiff struct {
	Model                      string          `json:"model"`
	InputTokensA               int64           `json:"input_tokens_a"`
	InputTokensB               int64           `json:"input_tokens_b"`
	InputTokenDelta            int64           `json:"input_token_delta"`
	ExpectedCostDeltaMicrosUSD int64           `json:"expected_cost_delta_micros_usd"`
	Components                 []ComponentDiff `json:"components,omitempty"`
}

type ComponentDiff struct {
	Modality string `json:"modality"`
	Unit     string `json:"unit"`
	ValueA   int64  `json:"value_a"`
	ValueB   int64  `json:"value_b"`
	Delta    int64  `json:"delta"`
}

type SourceDiff struct {
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	Changed []string `json:"changed,omitempty"`
}

type SourceCollector interface {
	Collect(context.Context, CollectRequest) (Collection, error)
}

type TokenCounter interface {
	Count(model Model, content []byte) (Count, error)
}

type ProviderVerifier interface {
	Verify(context.Context, Model, AssembledRequest) (VerificationResult, error)
}

type RunStore interface {
	Save(context.Context, Run) error
	Get(context.Context, string) (Run, error)
	List(context.Context, int) ([]RunSummary, error)
	Diff(context.Context, string, string) (RunDiff, error)
	Close() error
}

type AssembledRequest struct {
	System          string
	Tools           string
	Content         string
	Parts           []RequestPart
	AllowFileUpload bool
}

type RequestPart struct {
	AssetID string `json:"asset_id,omitempty"`
	Type    string `json:"type"`
	MIME    string `json:"mime,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Text    string `json:"-"`
	Data    []byte `json:"-"`
}

type VerificationResult struct {
	Tokens        int64
	Method        string
	Transport     string
	CleanupStatus string
}
