package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// ChartPin is one member app's release-resolved image pin -- the value
// build-helm-chart bakes into the chart's values.yaml imageTag, not the
// "latest" placeholder tools/helm/composer.go's hermetic Bazel action writes
// to the compose-time image lockfile (see
// //tools/app_registry/ARCHITECTURE.md "Chart -> image lockfile").
type ChartPin struct {
	AppFullName string
	Version     string
}

// ChartPinViolation is one pin CheckChartHermeticity rejected.
type ChartPinViolation struct {
	AppFullName string
	Version     string
	Reason      string
}

// ChartHermeticityChecker is AR-7f's (issue #558) compose-time gate: "a
// chart may only pin images the registry has published." Implemented as an
// interface so tests can substitute a fake instead of dialing a real
// registry -- see chart_hermeticity_test.go.
type ChartHermeticityChecker interface {
	// Check returns enforced=false when chartDomain's adoption stage is not
	// "allocate" (the per-domain gate; see ARCHITECTURE.md "Rejected
	// alternatives (issue #558)") -- callers must not fail the build in that
	// case, even if violations happens to be non-empty.
	Check(ctx context.Context, chartDomain string, pins []ChartPin) (enforced bool, violations []ChartPinViolation, err error)
}

// defaultHermeticityChecker is replaced in tests via withHermeticityChecker.
var defaultHermeticityChecker ChartHermeticityChecker = grpcHermeticityChecker{}

// grpcHermeticityChecker calls ArtifactRegistry.CheckChartHermeticity over a
// real gRPC connection, dialed fresh per call -- build-helm-chart runs once
// per chart in a release, not as a long-lived process, matching
// tools/app_registry/cli/cmd/client.go's "one dial per invocation" posture.
// This is the ONLY place in the compose-time chart pipeline that talks to a
// registry: tools/helm/composer.go (the Bazel action) never does, and this
// code never runs there either -- it is called from build-helm-chart, a
// release_helper_go CLI command that CI invokes as its own step, outside any
// Bazel action's sandbox. See ARCHITECTURE.md "Chart -> image lockfile" for
// why that split is load-bearing.
type grpcHermeticityChecker struct{}

func (grpcHermeticityChecker) Check(ctx context.Context, chartDomain string, pins []ChartPin) (bool, []ChartPinViolation, error) {
	client, closeConn, err := NewArtifactRegistryClient(ctx)
	if err != nil {
		return false, nil, err
	}
	defer closeConn() //nolint:errcheck

	req := &pb.CheckChartHermeticityRequest{ChartDomain: chartDomain}
	for _, p := range pins {
		req.Pins = append(req.Pins, &pb.ChartPin{AppFullName: p.AppFullName, Version: p.Version})
	}

	resp, err := client.CheckChartHermeticity(ctx, req)
	if err != nil {
		return false, nil, fmt.Errorf("CheckChartHermeticity: %w", err)
	}

	violations := make([]ChartPinViolation, 0, len(resp.Violations))
	for _, v := range resp.Violations {
		violations = append(violations, ChartPinViolation{AppFullName: v.AppFullName, Version: v.Version, Reason: v.Reason})
	}
	return resp.Enforced, violations, nil
}

func envOrDefault(key, fallback string) string {
	if v := defaultEnv(key); v != "" {
		return v
	}
	return fallback
}

// checkChartHermeticity is build-helm-chart's call site for the AR-7f gate.
// It is a no-op -- no registry client is even constructed -- unless
// APP_REGISTRY_CICD_OPT_IN is exactly "true", matching every other App
// Registry integration point in this repo (the bootstrap kill switch; see
// ARCHITECTURE.md "APP_REGISTRY_CICD_OPT_IN"). That is also what keeps this
// safe on a contributor's laptop or any environment with no registry
// access: the variable defaults unset there, so this function returns
// immediately without touching the network.
//
// A registry error (dial failure, auth failure, timeout) is deliberately
// NOT fatal: it is reported as a warning and the build proceeds. Only an
// actual CheckChartHermeticity response naming violations fails the build.
// This mirrors the "best-effort" posture every App Registry step in
// release.yml has at adoption stage observe/promote (continue-on-error) --
// AR-7f does not change that posture for the chart-compose step itself, it
// only adds a new failure mode (real violations at stage allocate) on top
// of it. No domain is at "allocate" today, so in practice this function
// currently never fails a build -- see PLAN.md's AR-7f "ships inert" note.
func checkChartHermeticity(ctx context.Context, warn func(string), chartDomain string, appVersions map[string]string) error {

	pins := make([]ChartPin, 0, len(appVersions))
	for appFullName, version := range appVersions {
		pins = append(pins, ChartPin{AppFullName: appFullName, Version: version})
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].AppFullName < pins[j].AppFullName })

	enforced, violations, err := defaultHermeticityChecker.Check(ctx, chartDomain, pins)
	if err != nil {
		warn(fmt.Sprintf("App Registry chart-hermeticity check skipped (registry error): %v", err))
		return nil
	}
	if !enforced || len(violations) == 0 {
		return nil
	}

	names := make([]string, 0, len(violations))
	for _, v := range violations {
		names = append(names, fmt.Sprintf("%s@%s (%s)", v.AppFullName, v.Version, v.Reason))
	}
	return fmt.Errorf("chart pins %d unpublished app(s), domain %q is at adoption stage 'allocate': %s",
		len(violations), chartDomain, strings.Join(names, ", "))
}
