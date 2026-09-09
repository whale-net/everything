package llm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writePriceTable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write price table %q: %v", path, err)
	}
}

func TestLoadPriceTable_ConvertsDecimalUSDToFixedPointMicros(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	writePriceTable(t, path, `{
		"openai/gpt-4o": {
			"prompt_usd_per_million": 2.5,
			"completion_usd_per_million": 10
		}
	}`)

	table, err := LoadPriceTable(path)
	if err != nil {
		t.Fatalf("LoadPriceTable: %v", err)
	}

	price, err := table.Lookup("openai/gpt-4o")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if price.PromptUSDPerMillion != 2_500_000 {
		t.Errorf("PromptUSDPerMillion = %d, want 2_500_000", price.PromptUSDPerMillion)
	}
	if price.CompletionUSDPerMillion != 10_000_000 {
		t.Errorf("CompletionUSDPerMillion = %d, want 10_000_000", price.CompletionUSDPerMillion)
	}
}

func TestPriceTable_Lookup_MissingEntryIsErrPriceNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	writePriceTable(t, path, `{}`)

	table, err := LoadPriceTable(path)
	if err != nil {
		t.Fatalf("LoadPriceTable: %v", err)
	}

	_, err = table.Lookup("openai/unknown-model")
	if err == nil {
		t.Fatalf("Lookup: got nil error for a model absent from the table")
	}
	var notFound *ErrPriceNotFound
	if !errors.As(err, &notFound) {
		t.Errorf("Lookup error = %v (%T), want *ErrPriceNotFound", err, err)
	}
}

// TestLoadPriceTable_ReloadsFromDiskWithoutCodeChange is the Testing
// section's "the price table reloads from config without a code change"
// case: LoadPriceTable does no caching of its own, so calling it again
// after the file on disk changes picks up the new contents with nothing
// but a second call.
func TestLoadPriceTable_ReloadsFromDiskWithoutCodeChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	writePriceTable(t, path, `{
		"openai/gpt-4o": {"prompt_usd_per_million": 2.5, "completion_usd_per_million": 10}
	}`)

	first, err := LoadPriceTable(path)
	if err != nil {
		t.Fatalf("LoadPriceTable (first): %v", err)
	}
	if _, err := first.Lookup("anthropic/claude-3.5-sonnet"); err == nil {
		t.Fatalf("Lookup: unexpectedly found a model before it was added to the table on disk")
	}

	// Edit the file on disk -- an operator changing prices, per LB6
	// ("contents and source stay cheap to change") -- with no code change
	// and no process restart, just a second LoadPriceTable call.
	writePriceTable(t, path, `{
		"openai/gpt-4o": {"prompt_usd_per_million": 3, "completion_usd_per_million": 12},
		"anthropic/claude-3.5-sonnet": {"prompt_usd_per_million": 3, "completion_usd_per_million": 15}
	}`)

	second, err := LoadPriceTable(path)
	if err != nil {
		t.Fatalf("LoadPriceTable (second): %v", err)
	}

	price, err := second.Lookup("openai/gpt-4o")
	if err != nil {
		t.Fatalf("Lookup(openai/gpt-4o) after edit: %v", err)
	}
	if price.PromptUSDPerMillion != 3_000_000 {
		t.Errorf("PromptUSDPerMillion after edit = %d, want 3_000_000 (picked up the on-disk change)", price.PromptUSDPerMillion)
	}

	if _, err := second.Lookup("anthropic/claude-3.5-sonnet"); err != nil {
		t.Errorf("Lookup(anthropic/claude-3.5-sonnet) after edit: %v (want the newly-added entry to be visible)", err)
	}
}

func TestLoadPriceTable_MissingFile(t *testing.T) {
	_, err := LoadPriceTable(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatalf("LoadPriceTable: got nil error for a nonexistent path")
	}
}

func TestLoadPriceTable_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	writePriceTable(t, path, `not json`)

	_, err := LoadPriceTable(path)
	if err == nil {
		t.Fatalf("LoadPriceTable: got nil error for invalid JSON")
	}
}
