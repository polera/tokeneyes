package tokeneyes

import (
	"testing"
	"time"
)

func TestFrontierModelsAreInDefaultCatalog(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		contextWindow int64
		maxOutput     int64
		inputPrice    int64
		cachedPrice   int64
		outputPrice   int64
	}{
		{name: "gpt-5.6", provider: "openai", contextWindow: 1_050_000, maxOutput: 128_000, inputPrice: 5_000_000, cachedPrice: 500_000, outputPrice: 30_000_000},
		{name: "gpt-5.6-terra", provider: "openai", contextWindow: 1_050_000, maxOutput: 128_000, inputPrice: 2_500_000, cachedPrice: 250_000, outputPrice: 15_000_000},
		{name: "gpt-5.6-luna", provider: "openai", contextWindow: 1_050_000, maxOutput: 128_000, inputPrice: 1_000_000, cachedPrice: 100_000, outputPrice: 6_000_000},
		{name: "gpt-5.4-nano", provider: "openai", contextWindow: 400_000, maxOutput: 128_000, inputPrice: 200_000, cachedPrice: 20_000, outputPrice: 1_250_000},
		{name: "claude-fable-5", provider: "anthropic", contextWindow: 1_000_000, maxOutput: 128_000, inputPrice: 10_000_000, cachedPrice: 1_000_000, outputPrice: 50_000_000},
		{name: "claude-opus-4-8", provider: "anthropic", contextWindow: 1_000_000, maxOutput: 128_000, inputPrice: 5_000_000, cachedPrice: 500_000, outputPrice: 25_000_000},
		{name: "claude-sonnet-5", provider: "anthropic", contextWindow: 1_000_000, maxOutput: 128_000, inputPrice: 2_000_000, cachedPrice: 200_000, outputPrice: 10_000_000},
		{name: "gemini-2.5-pro", provider: "google", contextWindow: 1_048_576, maxOutput: 65_536, inputPrice: 1_250_000, cachedPrice: 125_000, outputPrice: 10_000_000},
		{name: "gemini-2.5-flash", provider: "google", contextWindow: 1_048_576, maxOutput: 65_536, inputPrice: 300_000, cachedPrice: 30_000, outputPrice: 2_500_000},
		{name: "gemini-2.5-flash-lite", provider: "google", contextWindow: 1_048_576, maxOutput: 65_536, inputPrice: 100_000, cachedPrice: 10_000, outputPrice: 400_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, err := DefaultCatalog().Resolve(tt.name)
			if err != nil {
				t.Fatal(err)
			}
			if model.Provider != tt.provider || model.ContextWindow != tt.contextWindow || model.MaxOutput != tt.maxOutput {
				t.Fatalf("unexpected model metadata: %+v", model)
			}
			price := model.Price(0)
			if price.InputMicrosPerMTok != tt.inputPrice || price.CachedMicrosPerMTok != tt.cachedPrice || price.OutputMicrosPerMTok != tt.outputPrice {
				t.Fatalf("unexpected model pricing: %+v", price)
			}
		})
	}
}

func TestSonnet5IntroductoryPricingExpires(t *testing.T) {
	catalog := DefaultCatalog()
	model, err := catalog.Resolve("sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	if model.ID != "claude-sonnet-5" || model.Tokenizer != "claude-estimator-v2" {
		t.Fatalf("unexpected Sonnet 5 entry: %+v", model)
	}
	if model.PricingStale(time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("introductory pricing marked stale before expiration")
	}
	if !model.PricingStale(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("expired introductory pricing was not marked stale")
	}
}

func TestOpenAILongContextPricing(t *testing.T) {
	catalog := DefaultCatalog()
	canonical, err := catalog.Resolve("gpt-5.6")
	if err != nil {
		t.Fatal(err)
	}
	sol, err := catalog.Resolve("gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if sol.ID != canonical.ID {
		t.Fatalf("alias resolved to %q, want %q", sol.ID, canonical.ID)
	}

	boundary := canonical.Price(272_000)
	if boundary.InputMicrosPerMTok != 5_000_000 || boundary.CachedMicrosPerMTok != 500_000 || boundary.OutputMicrosPerMTok != 30_000_000 {
		t.Fatalf("unexpected boundary pricing: %+v", boundary)
	}

	long := canonical.Price(272_001)
	if long.InputMicrosPerMTok != 10_000_000 || long.CachedMicrosPerMTok != 1_000_000 || long.OutputMicrosPerMTok != 45_000_000 {
		t.Fatalf("unexpected long-context pricing: %+v", long)
	}

	gpt55, err := catalog.Resolve("gpt-5.5")
	if err != nil {
		t.Fatal(err)
	}
	if got := gpt55.Price(272_001); got.InputMicrosPerMTok != 10_000_000 || got.OutputMicrosPerMTok != 45_000_000 {
		t.Fatalf("GPT-5.5 long-context pricing missing: %+v", got)
	}
}

func TestGemini25PricingTiers(t *testing.T) {
	catalog := DefaultCatalog()
	pro, err := catalog.Resolve("gemini-2.5-pro")
	if err != nil {
		t.Fatal(err)
	}
	if got := pro.Price(200_000); got.InputMicrosPerMTok != 1_250_000 || got.OutputMicrosPerMTok != 10_000_000 {
		t.Fatalf("unexpected 200k boundary pricing: %+v", got)
	}
	if got := pro.Price(200_001); got.InputMicrosPerMTok != 2_500_000 || got.OutputMicrosPerMTok != 15_000_000 {
		t.Fatalf("unexpected long-context pricing: %+v", got)
	}

	flash, err := catalog.Resolve("gemini-2.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	if got := flash.PriceForModality("audio", 0); got.InputMicrosPerMTok != 1_000_000 || got.CachedMicrosPerMTok != 100_000 {
		t.Fatalf("unexpected audio pricing: %+v", got)
	}
}
