package tokeneyes

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const CatalogVersion = "2026-07-16"

type PriceTier struct {
	AboveInputTokens    int64 `json:"above_input_tokens" yaml:"above_input_tokens"`
	InputMicrosPerMTok  int64 `json:"input_micros_per_mtok" yaml:"input_micros_per_mtok"`
	CachedMicrosPerMTok int64 `json:"cached_micros_per_mtok" yaml:"cached_micros_per_mtok"`
	OutputMicrosPerMTok int64 `json:"output_micros_per_mtok" yaml:"output_micros_per_mtok"`
}

type Model struct {
	ID             string                 `json:"id" yaml:"id"`
	Aliases        []string               `json:"aliases" yaml:"aliases"`
	Provider       string                 `json:"provider" yaml:"provider"`
	Tokenizer      string                 `json:"tokenizer" yaml:"tokenizer"`
	ContextWindow  int64                  `json:"context_window" yaml:"context_window"`
	MaxOutput      int64                  `json:"max_output" yaml:"max_output"`
	PricingDate    string                 `json:"pricing_date" yaml:"pricing_date"`
	PricingExpires string                 `json:"pricing_expires,omitempty" yaml:"pricing_expires,omitempty"`
	Verification   string                 `json:"verification" yaml:"verification"`
	Tiers          []PriceTier            `json:"tiers" yaml:"tiers"`
	ModalityTiers  map[string][]PriceTier `json:"modality_tiers,omitempty" yaml:"modality_tiers,omitempty"`
	Media          MediaCapabilities      `json:"media" yaml:"media"`
}

type Catalog struct {
	Version string  `json:"version" yaml:"version"`
	Models  []Model `json:"models" yaml:"models"`
}

func DefaultCatalog() Catalog {
	return Catalog{Version: CatalogVersion, Models: []Model{
		{ID: "gpt-5.6", Aliases: []string{"gpt-5.6-sol"}, Provider: "openai", Tokenizer: "o200k_base", ContextWindow: 1_050_000, MaxOutput: 128_000, PricingDate: CatalogVersion, Verification: "none", Tiers: []PriceTier{{InputMicrosPerMTok: 5_000_000, CachedMicrosPerMTok: 500_000, OutputMicrosPerMTok: 30_000_000}, {AboveInputTokens: 272_000, InputMicrosPerMTok: 10_000_000, CachedMicrosPerMTok: 1_000_000, OutputMicrosPerMTok: 45_000_000}}, Media: openAIMedia(1, 0, 0, true)},
		{ID: "gpt-5.5", Aliases: []string{"codex", "openai"}, Provider: "openai", Tokenizer: "o200k_base", ContextWindow: 1_050_000, MaxOutput: 128_000, PricingDate: CatalogVersion, Verification: "none", Tiers: []PriceTier{{InputMicrosPerMTok: 5_000_000, CachedMicrosPerMTok: 500_000, OutputMicrosPerMTok: 30_000_000}}, Media: openAIMedia(1, 10_000, 6_000, true)},
		{ID: "gpt-5.4", Aliases: []string{"gpt"}, Provider: "openai", Tokenizer: "o200k_base", ContextWindow: 1_050_000, MaxOutput: 128_000, PricingDate: CatalogVersion, Verification: "none", Tiers: []PriceTier{{InputMicrosPerMTok: 2_500_000, CachedMicrosPerMTok: 250_000, OutputMicrosPerMTok: 15_000_000}, {AboveInputTokens: 272_000, InputMicrosPerMTok: 5_000_000, CachedMicrosPerMTok: 500_000, OutputMicrosPerMTok: 22_500_000}}, Media: openAIMedia(1, 10_000, 6_000, false)},
		{ID: "gpt-5.4-mini", Aliases: []string{"codex-mini"}, Provider: "openai", Tokenizer: "o200k_base", ContextWindow: 400_000, MaxOutput: 128_000, PricingDate: CatalogVersion, Verification: "none", Tiers: []PriceTier{{InputMicrosPerMTok: 750_000, CachedMicrosPerMTok: 75_000, OutputMicrosPerMTok: 4_500_000}}, Media: openAIMedia(1.62, 1_536, 0, false)},
		{ID: "claude-opus-4-8", Aliases: []string{"opus-4.8"}, Provider: "anthropic", Tokenizer: "claude-estimator-v2", ContextWindow: 1_000_000, MaxOutput: 128_000, PricingDate: CatalogVersion, Verification: "anthropic-count", Tiers: []PriceTier{{InputMicrosPerMTok: 5_000_000, CachedMicrosPerMTok: 500_000, OutputMicrosPerMTok: 25_000_000}}, Media: anthropicMedia(2_576, 4_784)},
		{ID: "claude-opus-4-7", Aliases: []string{"claude", "opus"}, Provider: "anthropic", Tokenizer: "claude-estimator-v2", ContextWindow: 1_000_000, MaxOutput: 128_000, PricingDate: CatalogVersion, Verification: "anthropic-count", Tiers: []PriceTier{{InputMicrosPerMTok: 5_000_000, CachedMicrosPerMTok: 500_000, OutputMicrosPerMTok: 25_000_000}}, Media: anthropicMedia(2_576, 4_784)},
		{ID: "claude-sonnet-5", Aliases: []string{"sonnet-5"}, Provider: "anthropic", Tokenizer: "claude-estimator-v2", ContextWindow: 1_000_000, MaxOutput: 128_000, PricingDate: CatalogVersion, PricingExpires: "2026-08-31", Verification: "anthropic-count", Tiers: []PriceTier{{InputMicrosPerMTok: 2_000_000, CachedMicrosPerMTok: 200_000, OutputMicrosPerMTok: 10_000_000}}, Media: anthropicMedia(2_576, 4_784)},
		{ID: "claude-sonnet-4-6", Aliases: []string{"sonnet"}, Provider: "anthropic", Tokenizer: "claude-estimator-v1", ContextWindow: 1_000_000, MaxOutput: 64_000, PricingDate: CatalogVersion, Verification: "anthropic-count", Tiers: []PriceTier{{InputMicrosPerMTok: 3_000_000, CachedMicrosPerMTok: 300_000, OutputMicrosPerMTok: 15_000_000}}, Media: anthropicMedia(1_568, 1_568)},
		{ID: "claude-haiku-4-5", Aliases: []string{"haiku"}, Provider: "anthropic", Tokenizer: "claude-estimator-v1", ContextWindow: 200_000, MaxOutput: 64_000, PricingDate: CatalogVersion, Verification: "anthropic-count", Tiers: []PriceTier{{InputMicrosPerMTok: 1_000_000, CachedMicrosPerMTok: 100_000, OutputMicrosPerMTok: 5_000_000}}, Media: anthropicMedia(1_568, 1_568)},
		{ID: "gemini-3.1-pro-preview", Aliases: []string{"gemini", "gemini-pro"}, Provider: "google", Tokenizer: "gemini-estimator-v1", ContextWindow: 1_048_576, MaxOutput: 65_536, PricingDate: CatalogVersion, Verification: "gemini-count", Tiers: []PriceTier{{InputMicrosPerMTok: 2_000_000, CachedMicrosPerMTok: 200_000, OutputMicrosPerMTok: 12_000_000}, {AboveInputTokens: 200_000, InputMicrosPerMTok: 4_000_000, CachedMicrosPerMTok: 400_000, OutputMicrosPerMTok: 18_000_000}}, Media: geminiMedia()},
		{ID: "gemini-3.5-flash", Aliases: []string{"gemini-flash"}, Provider: "google", Tokenizer: "gemini-estimator-v1", ContextWindow: 1_048_576, MaxOutput: 65_536, PricingDate: CatalogVersion, Verification: "gemini-count", Tiers: []PriceTier{{InputMicrosPerMTok: 750_000, CachedMicrosPerMTok: 80_000, OutputMicrosPerMTok: 4_500_000}}, Media: geminiMedia()},
		{ID: "gemini-3.1-flash-lite", Aliases: []string{"gemini-lite"}, Provider: "google", Tokenizer: "gemini-estimator-v1", ContextWindow: 1_048_576, MaxOutput: 65_536, PricingDate: CatalogVersion, Verification: "gemini-count", Tiers: []PriceTier{{InputMicrosPerMTok: 250_000, CachedMicrosPerMTok: 25_000, OutputMicrosPerMTok: 1_500_000}}, Media: geminiMedia()},
	}}
}

func LoadCatalog(override string) (Catalog, error) {
	c := DefaultCatalog()
	if override == "" {
		return c, c.Validate()
	}
	b, err := os.ReadFile(override)
	if err != nil {
		return Catalog{}, err
	}
	var extra Catalog
	if err := json.Unmarshal(b, &extra); err != nil {
		return Catalog{}, fmt.Errorf("catalog override must be JSON: %w", err)
	}
	for _, m := range extra.Models {
		replaced := false
		for i := range c.Models {
			if c.Models[i].ID == m.ID {
				c.Models[i] = m
				replaced = true
				break
			}
		}
		if !replaced {
			c.Models = append(c.Models, m)
		}
	}
	if extra.Version != "" {
		c.Version = extra.Version
	}
	return c, c.Validate()
}

func (c Catalog) Validate() error {
	for _, m := range c.Models {
		for _, modality := range m.Media.InputModalities {
			switch modality {
			case "text":
			case "image":
				if m.Media.Image == nil || m.Media.Image.FormulaVersion == "" || len(m.Media.Image.Details) == 0 {
					return fmt.Errorf("catalog %s: catalog_rule_missing for image", m.ID)
				}
			case "audio":
				if m.Media.Audio == nil || m.Media.Audio.FormulaVersion == "" || m.Media.Audio.TokensPerSecond <= 0 {
					return fmt.Errorf("catalog %s: catalog_rule_missing for audio", m.ID)
				}
			case "document":
				if m.Media.Document == nil || m.Media.Document.FormulaVersion == "" {
					return fmt.Errorf("catalog %s: catalog_rule_missing for document", m.ID)
				}
			default:
				return fmt.Errorf("catalog %s: unknown input modality %q", m.ID, modality)
			}
		}
	}
	return nil
}

func (c Catalog) Resolve(name string) (Model, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, m := range c.Models {
		if strings.ToLower(m.ID) == name {
			return m, nil
		}
		for _, a := range m.Aliases {
			if strings.ToLower(a) == name {
				return m, nil
			}
		}
	}
	return Model{}, fmt.Errorf("unknown model %q (use 'tokeneyes models list')", name)
}

func (c Catalog) SortedModels() []Model {
	out := append([]Model(nil), c.Models...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m Model) Price(inputTokens int64) PriceTier {
	var selected PriceTier
	for _, tier := range m.Tiers {
		if inputTokens >= tier.AboveInputTokens {
			selected = tier
		}
	}
	return selected
}

func (m Model) PriceForModality(modality string, inputTokens int64) PriceTier {
	tiers := m.ModalityTiers[modality]
	if len(tiers) == 0 {
		return m.Price(inputTokens)
	}
	var selected PriceTier
	for _, tier := range tiers {
		if inputTokens >= tier.AboveInputTokens {
			selected = tier
		}
	}
	return selected
}

func (m Model) PricingStale(now time.Time) bool {
	d, err := time.Parse("2006-01-02", m.PricingDate)
	if err != nil || now.Sub(d) > 180*24*time.Hour {
		return true
	}
	if m.PricingExpires == "" {
		return false
	}
	expires, err := time.Parse("2006-01-02", m.PricingExpires)
	if err != nil {
		return true
	}
	return !now.UTC().Before(expires.AddDate(0, 0, 1))
}
