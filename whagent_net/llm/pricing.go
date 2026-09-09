package llm

import (
	"encoding/json"
	"fmt"
	"os"
)

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

// priceTableFileEntry is one model's entry in the on-disk price table
// (see LoadPriceTable). Prices are authored in plain decimal USD per
// million tokens -- the format an operator copies straight out of
// OpenRouter's own pricing pages -- and converted to ModelPrice's
// fixed-point micro-dollar unit exactly once, at load time.
type priceTableFileEntry struct {
	PromptUSDPerMillion     float64 `json:"prompt_usd_per_million"`
	CompletionUSDPerMillion float64 `json:"completion_usd_per_million"`
}

// LoadPriceTable reads a price table from path (see
// whagent_net/ENV.md's WHAGENT_PRICE_TABLE_PATH). The file is a JSON
// object keyed on model id:
//
//	{
//	  "openai/gpt-4o": {
//	    "prompt_usd_per_million": 2.5,
//	    "completion_usd_per_million": 10
//	  }
//	}
//
// LoadPriceTable does no caching of its own -- every call re-reads path,
// so a price-table edit (LB6: "contents and source stay cheap to
// change") takes effect on the next call with no code change and no
// process restart required beyond however the caller chooses to invoke
// it.
func LoadPriceTable(path string) (*PriceTable, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("llm: load price table %q: %w", path, err)
	}

	var raw map[string]priceTableFileEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("llm: parse price table %q: %w", path, err)
	}

	prices := make(map[string]ModelPrice, len(raw))
	for model, entry := range raw {
		prices[model] = ModelPrice{
			PromptUSDPerMillion:     roundHalfAwayFromZero(entry.PromptUSDPerMillion * microDollarsPerUSD),
			CompletionUSDPerMillion: roundHalfAwayFromZero(entry.CompletionUSDPerMillion * microDollarsPerUSD),
		}
	}

	return &PriceTable{prices: prices}, nil
}

// Lookup returns model's price, or ErrPriceNotFound if the table has no
// entry for it.
func (t *PriceTable) Lookup(model string) (ModelPrice, error) {
	price, ok := t.prices[model]
	if !ok {
		return ModelPrice{}, &ErrPriceNotFound{Model: model}
	}
	return price, nil
}
