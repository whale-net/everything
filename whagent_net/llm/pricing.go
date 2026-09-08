package llm

import "fmt"

// ModelPrice is one model's per-token USD price, expressed as fixed-point
// USD-per-million-tokens in micro-dollar units -- the same precision
// CostUSD (cost.go) uses: PromptUSDPerMillion = 150_000 means $0.15 per
// 1,000,000 prompt tokens. Per-million is the unit providers publish
// prices in; a per-token unit would lose precision at micro-dollar
// granularity for the cheapest models.
type ModelPrice struct {
	PromptUSDPerMillion     int64
	CompletionUSDPerMillion int64
}

// ErrPriceNotFound is returned by PriceTable.Lookup for a model with no
// price-table entry. FR7/LB6: an unknown price is never treated as free
// -- ResolveCost surfaces this as an error the caller must handle, never
// a silent zero.
type ErrPriceNotFound struct {
	Model string
}

func (e *ErrPriceNotFound) Error() string {
	return fmt.Sprintf("no price-table entry for model %q", e.Model)
}

// PriceTable is a configurable per-model price table (LB6): "contents and
// source stay cheap to change" -- loaded fresh from config rather than
// hardcoded, so a model addition or a provider price change never needs
// a code change.
type PriceTable struct {
	prices map[string]ModelPrice
}

// LoadPriceTable reads a price table from path (see
// whagent_net/ENV.md's price-table variable for the config source this
// path comes from). Config format and reload behaviour land in the
// Implementation phase.
func LoadPriceTable(path string) (*PriceTable, error) {
	return nil, errNotImplemented
}

// Lookup returns model's price, or ErrPriceNotFound if the table has no
// entry for it.
func (t *PriceTable) Lookup(model string) (ModelPrice, error) {
	return ModelPrice{}, errNotImplemented
}
