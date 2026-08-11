package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeHermeticityChecker lets tests drive checkChartHermeticity's call site
// without dialing a real registry -- see ChartHermeticityChecker's doc
// comment on why it is an interface at all.
type fakeHermeticityChecker struct {
	enforced   bool
	violations []ChartPinViolation
	err        error

	calls int
	// recorded captures the (chartDomain, pins) of the last call for tests
	// that assert on what was sent, not just the response driving the
	// outcome.
	lastChartDomain string
	lastPins        []ChartPin
}

func (f *fakeHermeticityChecker) Check(_ context.Context, chartDomain string, pins []ChartPin) (bool, []ChartPinViolation, error) {
	f.calls++
	f.lastChartDomain = chartDomain
	f.lastPins = pins
	return f.enforced, f.violations, f.err
}

func withHermeticityChecker(c ChartHermeticityChecker, fn func()) {
	old := defaultHermeticityChecker
	defaultHermeticityChecker = c
	defer func() { defaultHermeticityChecker = old }()
	fn()
}

func noopWarn(string) {}

// TestCheckChartHermeticity_OptInOff proves the bootstrap-kill-switch no-op:
// with APP_REGISTRY_CICD_OPT_IN unset, the checker is never even called --
// not "called and ignored" -- so this is safe on a machine with no registry
// access at all.
func TestCheckChartHermeticity_OptInOff(t *testing.T) {
	fake := &fakeHermeticityChecker{enforced: true, violations: []ChartPinViolation{{AppFullName: "demo-app", Version: "v1.0.0", Reason: "not recorded"}}}
	withHermeticityChecker(fake, func() {
		withEnv(map[string]string{}, func() {
			err := checkChartHermeticity(context.Background(), noopWarn, "demo", map[string]string{"demo-app": "v1.0.0"})
			if err != nil {
				t.Fatalf("expected no-op with opt-in unset, got error: %v", err)
			}
		})
	})
	if fake.calls != 0 {
		t.Fatalf("expected checker never called with opt-in unset, got %d calls", fake.calls)
	}
}

// TestCheckChartHermeticity_OptInFalse is the explicit-false form of the
// same no-op -- release.yml's other App Registry steps gate on the string
// "true" specifically, and this must match.
func TestCheckChartHermeticity_OptInFalse(t *testing.T) {
	fake := &fakeHermeticityChecker{enforced: true, violations: []ChartPinViolation{{AppFullName: "demo-app", Version: "v1.0.0", Reason: "not recorded"}}}
	withHermeticityChecker(fake, func() {
		withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "false"}, func() {
			if err := checkChartHermeticity(context.Background(), noopWarn, "demo", map[string]string{"demo-app": "v1.0.0"}); err != nil {
				t.Fatalf("expected no-op with opt-in false, got error: %v", err)
			}
		})
	})
	if fake.calls != 0 {
		t.Fatalf("expected checker never called with opt-in false, got %d calls", fake.calls)
	}
}

// TestCheckChartHermeticity_NotEnforced is the observe/promote-stage
// regression this whole phase must not break: enforced=false must never
// fail the build, even if the fake is (incorrectly, hypothetically) handed
// violations alongside it.
func TestCheckChartHermeticity_NotEnforced(t *testing.T) {
	fake := &fakeHermeticityChecker{enforced: false, violations: []ChartPinViolation{{AppFullName: "demo-app", Version: "v1.0.0", Reason: "not recorded"}}}
	withHermeticityChecker(fake, func() {
		withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
			if err := checkChartHermeticity(context.Background(), noopWarn, "demo", map[string]string{"demo-app": "v1.0.0"}); err != nil {
				t.Fatalf("expected no-op when not enforced, got error: %v", err)
			}
		})
	})
	if fake.calls != 1 {
		t.Fatalf("expected checker called exactly once, got %d", fake.calls)
	}
}

// TestCheckChartHermeticity_ViolationNamesTheApp is AR-7f's exit criterion:
// an allocate-stage domain whose member app was never published fails,
// naming the offending app.
func TestCheckChartHermeticity_ViolationNamesTheApp(t *testing.T) {
	fake := &fakeHermeticityChecker{enforced: true, violations: []ChartPinViolation{
		{AppFullName: "demo-widget", Version: "v1.2.3", Reason: "not recorded in App Registry"},
	}}
	withHermeticityChecker(fake, func() {
		withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
			err := checkChartHermeticity(context.Background(), noopWarn, "demo", map[string]string{"demo-widget": "v1.2.3"})
			if err == nil {
				t.Fatal("expected error naming the unpublished app, got nil")
			}
			if !strings.Contains(err.Error(), "demo-widget") {
				t.Errorf("expected error to name demo-widget, got: %v", err)
			}
			if !strings.Contains(err.Error(), "v1.2.3") {
				t.Errorf("expected error to name the pinned version, got: %v", err)
			}
		})
	})
}

// TestCheckChartHermeticity_EnforcedNoViolations proves the success path:
// enforced=true with no violations builds cleanly.
func TestCheckChartHermeticity_EnforcedNoViolations(t *testing.T) {
	fake := &fakeHermeticityChecker{enforced: true}
	withHermeticityChecker(fake, func() {
		withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
			if err := checkChartHermeticity(context.Background(), noopWarn, "demo", map[string]string{"demo-app": "v1.0.0"}); err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
		})
	})
}

// TestCheckChartHermeticity_RegistryErrorIsNotFatal proves the fail-open
// posture on a transport/auth error: this must warn, not fail the chart
// build, so a registry outage never blocks a chart release on its own (see
// checkChartHermeticity's doc comment).
func TestCheckChartHermeticity_RegistryErrorIsNotFatal(t *testing.T) {
	fake := &fakeHermeticityChecker{err: fmt.Errorf("connection refused")}
	var warnings []string
	warn := func(msg string) { warnings = append(warnings, msg) }
	withHermeticityChecker(fake, func() {
		withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
			if err := checkChartHermeticity(context.Background(), warn, "demo", map[string]string{"demo-app": "v1.0.0"}); err != nil {
				t.Fatalf("expected registry errors to be non-fatal, got: %v", err)
			}
		})
	})
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one warning on registry error, got %v", warnings)
	}
}

// TestCheckChartHermeticity_PassesChartDomainAndPins locks in the wire
// contract with the registry: the chart's OWN domain is sent (not any
// member app's), and every pin's app_full_name/version round-trips.
func TestCheckChartHermeticity_PassesChartDomainAndPins(t *testing.T) {
	fake := &fakeHermeticityChecker{enforced: true}
	withHermeticityChecker(fake, func() {
		withEnv(map[string]string{"APP_REGISTRY_CICD_OPT_IN": "true"}, func() {
			appVersions := map[string]string{"demo-a": "v1.0.0", "demo-b": "v2.0.0"}
			if err := checkChartHermeticity(context.Background(), noopWarn, "demo", appVersions); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	})
	if fake.lastChartDomain != "demo" {
		t.Errorf("expected chart_domain %q, got %q", "demo", fake.lastChartDomain)
	}
	if len(fake.lastPins) != 2 {
		t.Fatalf("expected 2 pins, got %v", fake.lastPins)
	}
}
