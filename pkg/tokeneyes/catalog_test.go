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
		{name: "claude-opus-4-8", provider: "anthropic", contextWindow: 1_000_000, maxOutput: 128_000, inputPrice: 5_000_000, cachedPrice: 500_000, outputPrice: 25_000_000},
		{name: "claude-sonnet-5", provider: "anthropic", contextWindow: 1_000_000, maxOutput: 128_000, inputPrice: 2_000_000, cachedPrice: 200_000, outputPrice: 10_000_000},
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

func TestGPT56AliasesAndLongContextPricing(t *testing.T) {
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

	long := canonical.Price(272_000)
	if long.InputMicrosPerMTok != 10_000_000 || long.CachedMicrosPerMTok != 1_000_000 || long.OutputMicrosPerMTok != 45_000_000 {
		t.Fatalf("unexpected long-context pricing: %+v", long)
	}
}
