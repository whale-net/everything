package llm

// CostUSD is a fixed-point USD amount in micro-dollars (1 unit =
// $0.000001), matching the turn_usage.cost_usd NUMERIC(12,6) column
// (whagent_net/session, issue #2109). Arithmetic stays in this integer
// domain end to end (LB6/FR7: "never a float in the accumulate path") so
// summing many small per-turn costs cannot drift the way repeated
// float64 addition can.
type CostUSD int64

// microDollarsPerUSD is the fixed-point scale CostUSD and ModelPrice
// share: 1 USD = 1,000,000 micro-dollars.
const microDollarsPerUSD = 1_000_000

// Float64 converts to a plain USD float, for the one boundary that needs
// it: whagent_net/session.TurnUsage.CostUSD (#2109's UsageStore takes a
// float64 today) -- callers convert at that call site only, never
// mid-computation.
func (c CostUSD) Float64() float64 {
	return float64(c) / microDollarsPerUSD
}

// Add returns c + o. Exists so callers sum CostUSD values without
// dropping back to float64.
func (c CostUSD) Add(o CostUSD) CostUSD {
	return c + o
}

// usdToMicros converts a plain USD float (as OpenRouter reports its
// provider cost) to fixed-point micro-dollars, rounding to the nearest
// unit. This is the one place a provider-reported cost crosses from
// float to fixed point -- exactly once per turn, never accumulated in
// float form.
func usdToMicros(usd float64) CostUSD {
	return CostUSD(roundHalfAwayFromZero(usd * microDollarsPerUSD))
}

func roundHalfAwayFromZero(v float64) int64 {
	if v >= 0 {
		return int64(v + 0.5)
	}
	return int64(v - 0.5)
}

// UsageReport is the usage block Complete (client.go) returns from one
// provider response: token counts, and -- because usage.include=true is
// requested on every call (LB6) -- the provider's own reported cost and
// generation id when it supplies them. ProviderCostUSD is nil when the
// provider omitted cost, which ResolveCost treats as "must estimate",
// never as "free".
type UsageReport struct {
	PromptTokens     int64
	CompletionTokens int64
	ProviderCostUSD  *CostUSD
	GenerationID     string
}

// ResolveCost implements FR7/LB6 exactly:
//   - use the provider-reported cost when usage supplies one
//     (estimated=false);
//   - otherwise derive an estimate from prompt/completion token counts
//     against t's prices (estimated=true);
//   - a model with no provider cost and no price-table entry is an
//     ErrPriceNotFound the caller must surface -- ResolveCost never
//     returns a zero-as-unknown cost (the FR7 fail-open regression
//     case).
func (t *PriceTable) ResolveCost(usage UsageReport, model string) (cost CostUSD, estimated bool, err error) {
	if usage.ProviderCostUSD != nil {
		return *usage.ProviderCostUSD, false, nil
	}

	price, err := t.Lookup(model)
	if err != nil {
		// FR7 fail-open regression case: no provider cost and no price
		// entry is an error, never a silent 0/false/nil.
		return 0, false, err
	}

	estimate := tokenCost(usage.PromptTokens, price.PromptUSDPerMillion)
	estimate = estimate.Add(tokenCost(usage.CompletionTokens, price.CompletionUSDPerMillion))
	return estimate, true, nil
}

// tokenCost computes tokens priced at microPerMillion (micro-dollars per
// 1,000,000 tokens, ModelPrice's unit) entirely in fixed-point integer
// arithmetic, rounding the final division to the nearest micro-dollar.
func tokenCost(tokens int64, microPerMillion int64) CostUSD {
	return CostUSD(roundedDiv(tokens*microPerMillion, microDollarsPerUSD))
}

// roundedDiv divides num by den, rounding to the nearest integer
// (half away from zero) instead of truncating -- truncation alone would
// silently under-charge on every single turn, which compounds exactly
// the kind of drift LB6/FR7 rule out.
func roundedDiv(num, den int64) int64 {
	if den == 0 {
		return 0
	}
	half := den / 2
	if num >= 0 {
		return (num + half) / den
	}
	return (num - half) / den
}
