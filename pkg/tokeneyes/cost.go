package tokeneyes

import "fmt"

// CostMicros uses integer arithmetic throughout. Rates are micro-USD per one
// million tokens; half-up rounding produces the final micro-USD amount.
func CostMicros(rateMicrosPerMTok, tokens int64) int64 {
	if rateMicrosPerMTok <= 0 || tokens <= 0 {
		return 0
	}
	return (rateMicrosPerMTok*tokens + 500_000) / 1_000_000
}

func FormatUSD(micros int64) string {
	sign := ""
	if micros < 0 {
		sign = "-"
		micros = -micros
	}
	return fmt.Sprintf("%s%d.%06d", sign, micros/1_000_000, micros%1_000_000)
}

func ScenarioCost(model Model, input, cached, output, reasoning int64) int64 {
	return ScenarioCostBreakdown(model, input, cached, output, reasoning).Total()
}

func ScenarioCostBreakdown(model Model, input, cached, output, reasoning int64) CostBreakdown {
	if cached < 0 {
		cached = 0
	}
	if cached > input {
		cached = input
	}
	tier := model.Price(input)
	return CostBreakdown{
		InputMicrosUSD:       CostMicros(tier.InputMicrosPerMTok, input-cached),
		CachedInputMicrosUSD: CostMicros(tier.CachedMicrosPerMTok, cached),
		OutputMicrosUSD:      CostMicros(tier.OutputMicrosPerMTok, output),
		ReasoningMicrosUSD:   CostMicros(tier.OutputMicrosPerMTok, reasoning),
	}
}

// PriceComponents applies the catalog tier for each modality and attributes a
// caller-supplied cached-token amount in stable component order. The returned
// input categories exactly equal the sum recorded on the components plus the
// separately supplied request overhead.
func PriceComponents(model Model, components []CountComponent, overhead, cached, total int64) ([]CountComponent, int64, int64) {
	return PriceComponentsBound(model, components, overhead, cached, total, "expected")
}

// PriceComponentsBound prices text and media components using the same risk
// bound that selected the aggregate pricing tier.
func PriceComponentsBound(model Model, components []CountComponent, overhead, cached, total int64, bound string) ([]CountComponent, int64, int64) {
	out := append([]CountComponent(nil), components...)
	remaining := cached
	var regular, cachedCost int64
	for i := range out {
		if out[i].Unit != "tokens" {
			continue
		}
		n := out[i].Expected
		if bound == "high" {
			n = out[i].High
		}
		cachedN := min64(remaining, n)
		remaining -= cachedN
		tier := model.PriceForModality(out[i].Modality, total)
		out[i].CachedInputMicrosUSD = CostMicros(tier.CachedMicrosPerMTok, cachedN)
		out[i].InputMicrosUSD = CostMicros(tier.InputMicrosPerMTok, n-cachedN)
		regular += out[i].InputMicrosUSD
		cachedCost += out[i].CachedInputMicrosUSD
	}
	if overhead > 0 {
		cachedN := min64(remaining, overhead)
		tier := model.PriceForModality("text", total)
		cachedCost += CostMicros(tier.CachedMicrosPerMTok, cachedN)
		regular += CostMicros(tier.InputMicrosPerMTok, overhead-cachedN)
	}
	return out, regular, cachedCost
}
