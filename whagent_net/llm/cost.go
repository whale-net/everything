package llm

// CostUSD is a fixed-point USD amount in micro-dollars (1 unit =
// $0.000001), matching the turn_usage.cost_usd NUMERIC(12,6) column
// (whagent_net/session, issue #2109). Arithmetic stays in this integer
// domain end to end (LB6/FR7: "never a float in the accumulate path") so
// summing many small per-turn costs cannot drift the way repeated
// float64 addition can.
type CostUSD int64

// Float64 converts to a plain USD float, for the one boundary that needs
// it: whagent_net/session.TurnUsage.CostUSD (#2109's UsageStore takes a
// float64 today) -- callers convert at that call site only, never
// mid-computation.
func (c CostUSD) Float64() float64 {
	return float64(c) / 1_000_000
}

// Add returns c + o. Exists so callers sum CostUSD values without
// dropping back to float64.
func (c CostUSD) Add(o CostUSD) CostUSD {
	return c + o
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
	return 0, false, errNotImplemented
}
