package tokeneyes

import "testing"

func TestCostUsesFixedPointCategories(t *testing.T) {
	m, _ := DefaultCatalog().Resolve("gpt-5.4-mini")
	got := ScenarioCost(m, 1_000_000, 500_000, 1_000_000, 0)
	// 0.5M regular input ($0.375) + 0.5M cached ($0.0375) + 1M output ($4.50).
	if got != 4_912_500 {
		t.Fatalf("cost=%d micro-USD, want 4912500", got)
	}
	if FormatUSD(got) != "4.912500" {
		t.Fatalf("format=%q", FormatUSD(got))
	}
}

func TestLongContextTier(t *testing.T) {
	m, _ := DefaultCatalog().Resolve("gpt-5.4")
	below := m.Price(272_000 - 1)
	above := m.Price(272_000)
	if below.InputMicrosPerMTok != 2_500_000 || above.InputMicrosPerMTok != 5_000_000 {
		t.Fatalf("wrong tiers: below=%+v above=%+v", below, above)
	}
}
