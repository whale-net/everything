package llm

import (
	"errors"
	"testing"
)

func TestResolveCost_ProviderCostReturnedVerbatim(t *testing.T) {
	table := &PriceTable{prices: map[string]ModelPrice{}} // deliberately empty: provider cost must win without consulting the table at all
	provided := CostUSD(4_200_000)
	usage := UsageReport{PromptTokens: 1000, CompletionTokens: 500, ProviderCostUSD: &provided}

	cost, estimated, err := table.ResolveCost(usage, "openai/gpt-4o")
	if err != nil {
		t.Fatalf("ResolveCost: %v", err)
	}
	if estimated {
		t.Errorf("estimated = true, want false when the provider supplied a cost")
	}
	if cost != provided {
		t.Errorf("cost = %d, want %d (verbatim provider cost)", cost, provided)
	}
}

func TestResolveCost_EstimatesFromPriceTableWhenNoProviderCost(t *testing.T) {
	table := &PriceTable{prices: map[string]ModelPrice{
		"openai/gpt-4o": {
			PromptUSDPerMillion:     2_500_000,  // $2.50 / 1M prompt tokens
			CompletionUSDPerMillion: 10_000_000, // $10.00 / 1M completion tokens
		},
	}}
	usage := UsageReport{PromptTokens: 1_000, CompletionTokens: 500}

	cost, estimated, err := table.ResolveCost(usage, "openai/gpt-4o")
	if err != nil {
		t.Fatalf("ResolveCost: %v", err)
	}
	if !estimated {
		t.Errorf("estimated = false, want true when the provider supplied no cost")
	}
	// 1000 * 2_500_000 / 1_000_000 = 2500 (prompt) + 500 * 10_000_000 / 1_000_000 = 5000 (completion) = 7500 micro-dollars.
	want := CostUSD(7_500)
	if cost != want {
		t.Errorf("cost = %d, want %d", cost, want)
	}
}

// TestResolveCost_NoProviderCostAndNoPriceEntry_IsError is the FR7
// fail-open regression test: a model with no provider cost and no
// price-table entry must be a surfaced error, never a silent 0/false/nil
// that a cap-enforcement caller could mistake for "free".
func TestResolveCost_NoProviderCostAndNoPriceEntry_IsError(t *testing.T) {
	table := &PriceTable{prices: map[string]ModelPrice{}}
	usage := UsageReport{PromptTokens: 1_000, CompletionTokens: 500}

	cost, estimated, err := table.ResolveCost(usage, "openai/unknown-model")
	if err == nil {
		t.Fatalf("ResolveCost: got nil error for a model absent from the price table with no provider cost")
	}
	var notFound *ErrPriceNotFound
	if !errors.As(err, &notFound) {
		t.Errorf("ResolveCost error = %v (%T), want *ErrPriceNotFound", err, err)
	}
	if cost != 0 || estimated != false {
		t.Errorf("ResolveCost on error returned (cost=%d, estimated=%v), want the zero values alongside the error (never treated as a meaningful result)", cost, estimated)
	}
}

func TestCostUSD_FixedPointSumDoesNotDrift(t *testing.T) {
	// LB6/FR7: "never a float in the accumulate path" -- summing many
	// small per-turn costs in CostUSD's fixed-point domain must match an
	// exact expected total, unlike repeated float64 addition (e.g.
	// 0.1 + 0.2 + ... in float64 accumulates rounding error).
	const perTurn = CostUSD(333) // $0.000333, an amount not exactly representable in binary floating point
	const turns = 10_000

	var total CostUSD
	for i := 0; i < turns; i++ {
		total = total.Add(perTurn)
	}

	want := CostUSD(perTurn * turns)
	if total != want {
		t.Fatalf("total = %d, want exactly %d (fixed-point summation must not drift)", total, want)
	}
	if total.Float64() != 3.33 {
		t.Fatalf("total.Float64() = %v, want exactly 3.33", total.Float64())
	}
}

func TestTokenCost_RoundsToNearestMicroDollar(t *testing.T) {
	// 3 tokens * 500_000 micro-dollars/million = 1_500_000 / 1_000_000 =
	// 1.5, which truncation would floor to 1 but must round to 2.
	got := tokenCost(3, 500_000)
	if got != 2 {
		t.Errorf("tokenCost(3, 500_000) = %d, want 2 (rounded, not truncated)", got)
	}
}
