package release

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// writeFakePlanBinary writes an executable shell script standing in for
// release_helper_go, so ResolvePlan's tests exercise the real
// exec.CommandContext shell-out path (args, cmd.Dir, WorkspaceRoot
// handling) without depending on the real binary being built or on PATH in
// the test sandbox. The script records the args it was invoked with to
// argsFile (one per line) and prints stdout verbatim -- these tests assert
// on ResolvePlan's own behavior (the metadata JSON it builds, whether
// a.WorkspaceRoot is required), not on release_helper_go's real
// version-resolution logic, which tools/release_helper_go/cmd/plan_test.go
// already covers.
func writeFakePlanBinary(t *testing.T, stdout string) (binPath, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	binPath = filepath.Join(dir, "fake-release-helper-go")
	argsFile = filepath.Join(dir, "args.txt")
	script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do printf '%%s\\n' \"$a\"; done > %q\ncat <<'PLANOUT'\n%s\nPLANOUT\n", argsFile, stdout)
	require.NoError(t, os.WriteFile(binPath, []byte(script), 0o755))
	return binPath, argsFile
}

func readFakeArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// runResolvePlan executes a.ResolvePlan through a real Temporal activity
// environment rather than calling it directly with context.Background():
// ResolvePlan calls activity.GetInfo(ctx) (for the idempotency-key-prefix),
// which panics outside a real activity context -- testsuite's
// TestActivityEnvironment is the SDK's own way to unit test an activity
// method that does this, mirroring workflow_test.go's use of
// testsuite.WorkflowTestSuite for full-workflow tests.
func runResolvePlan(t *testing.T, a *Activities, targets []ReleaseTarget) (ResolvedPlan, error) {
	t.Helper()
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestActivityEnvironment()
	env.RegisterActivity(a.ResolvePlan)
	val, err := env.ExecuteActivity(a.ResolvePlan, targets)
	if err != nil {
		return ResolvedPlan{}, err
	}
	var plan ResolvedPlan
	require.NoError(t, val.Get(&plan))
	return plan, nil
}

// TestActivities_ResolvePlan_WorkspaceRootUnset_UsesScratchDir is the direct
// regression test for the prod failure this fix addresses: a release
// trigger against run 5b3d2e4c-1685-43b9-9dd4-863fe01fecb1 failed with
// "WorkspaceRoot not configured" because ResolvePlan hard-required a full
// monorepo checkout. It no longer does -- Domain/Name/AppType come from
// a.Registry, not bazel discovery, so a.WorkspaceRoot being empty must
// succeed via a scratch temp dir.
func TestActivities_ResolvePlan_WorkspaceRootUnset_UsesScratchDir(t *testing.T) {
	repo := newTestRegistry(t)
	_, err := repo.Reconcile(context.Background(), []*appmetapb.AppManifest{
		{Domain: "demo", Name: "hello-go", AppType: "external-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
	}, nil, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)

	bin, argsFile := writeFakePlanBinary(t, `{"versions":{"demo-hello-go":"v1.2.4"}}`)
	a := &Activities{Registry: repo, PlanBinaryPath: bin}

	plan, err := runResolvePlan(t, a, []ReleaseTarget{
		{OwnerFullName: "demo-hello-go", Kind: repository.ArtifactKindImage},
	})
	require.NoError(t, err, "WorkspaceRoot unset must not hard-fail -- ResolvePlan should fall back to a scratch temp dir")
	require.Equal(t, "v1.2.4", plan.Versions["image:demo-hello-go"])

	args := readFakeArgs(t, argsFile)
	joined := strings.Join(args, " ")
	require.Contains(t, joined, "--apps-metadata=", "must use the bazel-free metadata path, not --apps=<names>")
	require.Contains(t, joined, `"domain":"demo"`)
	require.Contains(t, joined, `"name":"hello-go"`)
	require.Contains(t, joined, `"app_type":"external-api"`)
	require.NotContains(t, joined, "--apps=demo-hello-go")
}

// TestActivities_ResolvePlan_ChartTarget_LooksUpChartMetadata mirrors the
// image case for a chart target.
func TestActivities_ResolvePlan_ChartTarget_LooksUpChartMetadata(t *testing.T) {
	repo := newTestRegistry(t)
	_, err := repo.Reconcile(context.Background(), nil, []*appmetapb.ChartManifest{
		{Domain: "manmanv2", Name: "control-services"},
	}, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)

	bin, argsFile := writeFakePlanBinary(t, `{"versions":{"manmanv2-control-services":"v2.0.1"}}`)
	a := &Activities{Registry: repo, PlanBinaryPath: bin}

	plan, err := runResolvePlan(t, a, []ReleaseTarget{
		{OwnerFullName: "manmanv2-control-services", Kind: repository.ArtifactKindChart},
	})
	require.NoError(t, err)
	require.Equal(t, "v2.0.1", plan.Versions["chart:manmanv2-control-services"])

	joined := strings.Join(readFakeArgs(t, argsFile), " ")
	require.Contains(t, joined, "--charts-metadata=")
	require.Contains(t, joined, `"domain":"manmanv2"`)
	require.Contains(t, joined, `"name":"control-services"`)
	require.NotContains(t, joined, "--charts=manmanv2-control-services")
}

// TestActivities_ResolvePlan_UnknownOwner_ReturnsClearError proves a
// target with no matching App Registry row fails with a clear,
// identifiable error rather than a confusing downstream CLI failure.
func TestActivities_ResolvePlan_UnknownOwner_ReturnsClearError(t *testing.T) {
	repo := newTestRegistry(t)
	a := &Activities{Registry: repo, PlanBinaryPath: "unused"}

	_, err := runResolvePlan(t, a, []ReleaseTarget{
		{OwnerFullName: "demo-nonexistent", Kind: repository.ArtifactKindImage},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "no App Registry app named")
}

// TestActivities_ResolvePlan_ForcesRegistryOptIn is the regression test for
// the bug where every Temporal-driven release batch silently resolved every
// target to v0.0.1 regardless of its actual App Registry version history:
// release_helper_go's registryOptedIn()
// (cmd/plan.go) gates the whole AllocateVersion path on
// APP_REGISTRY_CICD_OPT_IN=true, which this worker's own process env never
// carried (it's documented purely as a GitHub Actions repository variable) --
// so the subprocess always fell through to the git-tag tagFallback, which is
// always broken in ResolvePlan's git-less scratch dir. ResolvePlan must force
// this var into the subprocess's env unconditionally.
func TestActivities_ResolvePlan_ForcesRegistryOptIn(t *testing.T) {
	repo := newTestRegistry(t)
	_, err := repo.Reconcile(context.Background(), []*appmetapb.AppManifest{
		{Domain: "demo", Name: "hello-go", AppType: "external-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
	}, nil, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)

	dir := t.TempDir()
	binPath := filepath.Join(dir, "fake-release-helper-go")
	envFile := filepath.Join(dir, "env.txt")
	script := fmt.Sprintf("#!/bin/sh\nenv > %q\ncat <<'PLANOUT'\n{\"versions\":{\"demo-hello-go\":\"v1.0.0\"}}\nPLANOUT\n", envFile)
	require.NoError(t, os.WriteFile(binPath, []byte(script), 0o755))

	// A pre-existing false value in the parent env must not survive --
	// ResolvePlan's forced value must win, not merely default when unset.
	t.Setenv("APP_REGISTRY_CICD_OPT_IN", "false")

	a := &Activities{Registry: repo, PlanBinaryPath: binPath}
	plan, err := runResolvePlan(t, a, []ReleaseTarget{
		{OwnerFullName: "demo-hello-go", Kind: repository.ArtifactKindImage},
	})
	require.NoError(t, err)
	require.Equal(t, "v1.0.0", plan.Versions["image:demo-hello-go"])

	envOut, err := os.ReadFile(envFile)
	require.NoError(t, err)
	require.Contains(t, string(envOut), "APP_REGISTRY_CICD_OPT_IN=true")
}

// TestActivities_ResolvePlan_IdempotencyKeyUsesRunID_NotWorkflowID is the
// direct regression test for a real prod bug: the "tools" domain's version
// resolution silently returned a stale v0.5.0 forever, no matter how many
// hours passed or how many other successful releases of the same targets
// happened via other paths -- confirmed against prod's
// data.idempotency_key: the row
// "release-5ac1b1d5...-tools-app-registry-allocate" was created once (at
// 2026-08-23T03:20:59Z) and kept being replayed by every later trigger.
//
// Root cause: WorkflowID (workflow.go) is deliberately deterministic per
// target batch -- the same "release tools domain" request always hashes to
// the same WorkflowID, by design, so Temporal's own
// WorkflowExecutionAlreadyStarted rejection can enforce "at most one
// non-terminal release per target" (that dedup guarantee is correct and
// still wanted). But ResolvePlan used that same reused-by-design ID as its
// AllocateVersion idempotency-key-prefix, so every later, genuinely
// distinct trigger of the same batch (each a fresh execution, with a fresh
// RunID, once the prior execution reached a terminal state) replayed the
// very first execution's cached response instead of allocating a real next
// version. It must use WorkflowExecution.RunID (unique per execution)
// instead -- RunID still correctly dedupes true same-execution activity
// retries (the actual property this idempotency key exists for, per this
// file's package doc comment on ResolvePlan).
//
// testsuite.TestActivityEnvironment fixes both WorkflowExecution.ID
// ("default-test-workflow-id") and RunID ("default-test-run-id") to
// distinct constants with no way to vary them per environment -- exactly
// what makes this assertion meaningful: it fails if ResolvePlan reads .ID
// instead of .RunID, and passes only when it reads the right field.
func TestActivities_ResolvePlan_IdempotencyKeyUsesRunID_NotWorkflowID(t *testing.T) {
	repo := newTestRegistry(t)
	_, err := repo.Reconcile(context.Background(), []*appmetapb.AppManifest{
		{Domain: "demo", Name: "hello-go", AppType: "external-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
	}, nil, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)

	bin, argsFile := writeFakePlanBinary(t, `{"versions":{"demo-hello-go":"v1.2.4"}}`)
	a := &Activities{Registry: repo, PlanBinaryPath: bin}

	_, err = runResolvePlan(t, a, []ReleaseTarget{
		{OwnerFullName: "demo-hello-go", Kind: repository.ArtifactKindImage},
	})
	require.NoError(t, err)

	joined := strings.Join(readFakeArgs(t, argsFile), " ")
	require.Contains(t, joined, "--idempotency-key-prefix=default-test-run-id",
		"must key on the Temporal WorkflowExecution *run* id, which is unique per execution")
	require.NotContains(t, joined, "--idempotency-key-prefix=default-test-workflow-id",
		"must NOT key on the Temporal workflow id, which is deliberately reused across every distinct trigger of the same target batch (see WorkflowID's doc comment) -- doing so replays a stale cached AllocateVersion response forever")
}

// TestActivities_ResolvePlan_WorkspaceRootSet_UsedAsIs proves an explicit
// a.WorkspaceRoot (e.g. an operator override) is still honored as cmd.Dir
// rather than always forcing a scratch dir.
func TestActivities_ResolvePlan_WorkspaceRootSet_UsedAsIs(t *testing.T) {
	repo := newTestRegistry(t)
	_, err := repo.Reconcile(context.Background(), []*appmetapb.AppManifest{
		{Domain: "demo", Name: "hello-go", AppType: "external-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
	}, nil, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)

	bin, _ := writeFakePlanBinary(t, `{"versions":{"demo-hello-go":"v1.0.0"}}`)
	explicitRoot := t.TempDir()
	a := &Activities{Registry: repo, PlanBinaryPath: bin, WorkspaceRoot: explicitRoot}

	plan, err := runResolvePlan(t, a, []ReleaseTarget{
		{OwnerFullName: "demo-hello-go", Kind: repository.ArtifactKindImage},
	})
	require.NoError(t, err)
	require.Equal(t, "v1.0.0", plan.Versions["image:demo-hello-go"])
}

// TestActivities_ResolvePlan_NoVersionSelections_PassesIncrementPatch proves
// the pre-existing default is unchanged for callers that never set
// ReleaseTarget.VersionSelection (e.g. anything still on the old free-form-
// scope path): ResolvePlan must still pass the batch-wide --increment-patch
// flag, with no --version-selections at all.
func TestActivities_ResolvePlan_NoVersionSelections_PassesIncrementPatch(t *testing.T) {
	repo := newTestRegistry(t)
	_, err := repo.Reconcile(context.Background(), []*appmetapb.AppManifest{
		{Domain: "demo", Name: "hello-go", AppType: "external-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
	}, nil, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)

	bin, argsFile := writeFakePlanBinary(t, `{"versions":{"demo-hello-go":"v1.2.5"}}`)
	a := &Activities{Registry: repo, PlanBinaryPath: bin}

	_, err = runResolvePlan(t, a, []ReleaseTarget{
		{OwnerFullName: "demo-hello-go", Kind: repository.ArtifactKindImage},
	})
	require.NoError(t, err)

	joined := strings.Join(readFakeArgs(t, argsFile), " ")
	require.Contains(t, joined, "--increment-patch")
	require.NotContains(t, joined, "--version-selections=")
}

// TestActivities_ResolvePlan_MixedVersionSelections_BuildsPerTargetJSON is
// the direct regression test for the release-trigger UI's per-target
// Draft-page picker (issue #889 follow-up): a batch with one target on an
// explicit hardcoded version, one on a specific bump type, and one with no
// selection at all must build --version-selections covering exactly the
// first two, and must still pass --increment-patch as the batch-wide
// default for the third (uncovered) target.
func TestActivities_ResolvePlan_MixedVersionSelections_BuildsPerTargetJSON(t *testing.T) {
	repo := newTestRegistry(t)
	_, err := repo.Reconcile(context.Background(), []*appmetapb.AppManifest{
		{Domain: "demo", Name: "hello-go", AppType: "external-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
		{Domain: "demo", Name: "worker", AppType: "worker", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
		{Domain: "demo", Name: "job", AppType: "job", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
	}, nil, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)

	bin, argsFile := writeFakePlanBinary(t, `{"versions":{"demo-hello-go":"v9.9.9","demo-worker":"v1.1.0","demo-job":"v0.0.2"}}`)
	a := &Activities{Registry: repo, PlanBinaryPath: bin}

	_, err = runResolvePlan(t, a, []ReleaseTarget{
		{OwnerFullName: "demo-hello-go", Kind: repository.ArtifactKindImage, VersionSelection: "v9.9.9"},
		{OwnerFullName: "demo-worker", Kind: repository.ArtifactKindImage, VersionSelection: "minor"},
		{OwnerFullName: "demo-job", Kind: repository.ArtifactKindImage},
	})
	require.NoError(t, err)

	joined := strings.Join(readFakeArgs(t, argsFile), " ")
	require.Contains(t, joined, "--increment-patch", "demo-job has no selection, so the batch-wide default must still be passed")
	require.Contains(t, joined, "--version-selections=")
	require.Contains(t, joined, `"demo-hello-go":"v9.9.9"`)
	require.Contains(t, joined, `"demo-worker":"minor"`)
	require.NotContains(t, joined, `"demo-job"`, "demo-job has no VersionSelection, so it must not appear in --version-selections at all")
}
