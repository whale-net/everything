package components

import (
	"context"
	"strings"
	"testing"
)

func renderCostPage(t *testing.T, data CostPageData) string {
	t.Helper()
	var buf strings.Builder
	if err := Cost(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Cost render failed: %v", err)
	}
	return buf.String()
}

func TestCost_RendersControlsRowsAndTotal(t *testing.T) {
	out := renderCostPage(t, CostPageData{
		Filters: CostFilters{Period: "week", ByAgent: true},
		Rows:    []CostRowView{{PeriodLabel: "2024-W01", Agent: "a1", Turns: 3, CostUSD: 1.5, Estimated: true}},
		Total:   CostRowView{Turns: 3, CostUSD: 1.5},
	})
	for _, want := range []string{"Cost (UTC)", `hx-push-url="true"`, `<option value="week" selected`, "2024-W01", "$1.5000", "Total"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output", want)
		}
	}
	if strings.Count(out, ">estimated<") != 1 {
		t.Errorf("want exactly one estimated badge (row only, total not estimated)")
	}
	if !strings.Contains(out, `name="agent" value="1" class="toggle toggle-sm" checked`) {
		t.Errorf("agent toggle should be checked")
	}
	if strings.Contains(out, `name="model" value="1" class="toggle toggle-sm" checked`) {
		t.Errorf("model toggle should be unchecked")
	}
}

func TestCostTable_EmptyAndError(t *testing.T) {
	var buf strings.Builder
	if err := CostTable(CostPageData{}).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No usage recorded") || strings.Contains(buf.String(), "<table") {
		t.Errorf("empty state wrong: %s", buf.String())
	}
	buf.Reset()
	if err := CostTable(CostPageData{FetchError: "boom"}).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("error not shown")
	}
}
