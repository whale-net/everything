package pages

import (
	"context"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// --- VIA_CHART auto-select (trigger-release picker implies its owning
// chart(s), so releasing a VIA_CHART app's image alone doesn't leave the
// chart un-released) --------------------------------------------------------

func TestBuildDomainPickerGroups_ViaChartAppImpliesOwningChartToken(t *testing.T) {
	apps := []*pb.App{
		{AppId: "app-1", Domain: "platform", FullName: "platform-web-api", Name: "web-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART},
	}
	charts := []*pb.Chart{
		{ChartId: "chart-1", Domain: "platform", FullName: "platform-web", Name: "web", AppIds: []string{"app-1"}},
	}

	groups := BuildDomainPickerGroups(apps, charts)
	if len(groups) != 1 {
		t.Fatalf("expected 1 domain group, got %d", len(groups))
	}
	if len(groups[0].Apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(groups[0].Apps))
	}
	got := groups[0].Apps[0].ImpliedChartTokens
	want := []string{PickerToken(PickerKindChart, "platform-web")}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("ImpliedChartTokens = %v, want %v", got, want)
	}
}

func TestBuildDomainPickerGroups_NonViaChartAppHasNoImpliedChart(t *testing.T) {
	apps := []*pb.App{
		{AppId: "app-1", Domain: "platform", FullName: "platform-worker", Name: "worker", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
	}
	charts := []*pb.Chart{
		{ChartId: "chart-1", Domain: "platform", FullName: "platform-web", Name: "web", AppIds: []string{"app-1"}},
	}

	groups := BuildDomainPickerGroups(apps, charts)
	got := groups[0].Apps[0].ImpliedChartTokens
	if len(got) != 0 {
		t.Errorf("expected no implied chart tokens for a non-VIA_CHART app, got %v", got)
	}
}

func TestBuildDomainPickerGroups_ViaChartAppWithNoOwningChartHasNoImpliedChart(t *testing.T) {
	// Defensive: a VIA_CHART app whose owning chart isn't in the fetched
	// catalog (e.g. a partial/stale read) must not panic and must simply
	// imply nothing.
	apps := []*pb.App{
		{AppId: "app-1", Domain: "platform", FullName: "platform-web-api", Name: "web-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART},
	}

	groups := BuildDomainPickerGroups(apps, nil)
	got := groups[0].Apps[0].ImpliedChartTokens
	if len(got) != 0 {
		t.Errorf("expected no implied chart tokens when no chart claims this app, got %v", got)
	}
}

func TestDomainPickerGroupView_WiresAutoSelectOnlyForViaChartApp(t *testing.T) {
	apps := []*pb.App{
		{AppId: "app-1", Domain: "platform", FullName: "platform-web-api", Name: "web-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART},
		{AppId: "app-2", Domain: "platform", FullName: "platform-worker", Name: "worker", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
	}
	charts := []*pb.Chart{
		{ChartId: "chart-1", Domain: "platform", FullName: "platform-web", Name: "web", AppIds: []string{"app-1"}},
	}

	groups := BuildDomainPickerGroups(apps, charts)

	var buf strings.Builder
	if err := domainPickerGroupView(groups[0], map[string]bool{}).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, "autoSelectRelatedCharts") {
		t.Fatalf("expected the VIA_CHART app's checkbox to wire autoSelectRelatedCharts, body: %s", body)
	}
	if !strings.Contains(body, `onchange="__templ_autoSelectRelatedCharts`) {
		t.Fatalf("expected an onchange handler calling the generated autoSelectRelatedCharts script, body: %s", body)
	}

	// The non-VIA_CHART app's checkbox (value="app:platform-worker") must
	// not carry the auto-select wiring -- only the VIA_CHART row (just
	// asserted above) does. There's exactly one onchange wiring in the
	// whole group, proving the second app's row doesn't get one.
	if n := strings.Count(body, `onchange="__templ_autoSelectRelatedCharts`); n != 1 {
		t.Errorf("expected exactly 1 autoSelectRelatedCharts onchange wiring (VIA_CHART app only), got %d, body: %s", n, body)
	}
}
