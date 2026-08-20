// plan.go implements ResolvePlan's interim real implementation: shelling
// out to the existing `release_helper_go plan` binary rather than
// extracting tools/release_helper_go/cmd/plan.go's planRelease into a
// library function callable in-process.
//
// # Why shell out instead of extracting a library
//
// planRelease (tools/release_helper_go/cmd/plan.go) takes a planParams
// struct wired to a BazelRunner/GitRunner/FileSystem trio and a
// workspaceRoot -- its version-resolution logic (auto-increment via git
// tags, change detection via `bazel query`, App Registry version
// allocation) fundamentally assumes it is running inside a full monorepo
// checkout with `bazel` and `git` on PATH, exactly release.yml's
// plan-release job's environment. Reaching in and extracting just the
// version-resolution slice of that ~850-line file without its Bazel/git
// dependencies is a real refactor, not a mechanical export -- issue #889's
// Implementation scope explicitly allows exactly this fallback: "If
// extraction is non-trivial, scaffold this activity to shell out to the
// existing release_helper_go plan binary as an interim implementation and
// leave a comment noting the library-extraction as a documented
// follow-up." This is that interim implementation; the follow-up is
// extracting planRelease's core into a package importable from here
// without a subprocess or a workspace checkout.
//
// # Operational requirement (documented, not solved here)
//
// Because of the above, ResolvePlan requires the app-registry-worker
// process to have both a `release_helper_go` binary (PlanBinaryPath) and a
// full monorepo checkout with `bazel`/`git` available (WorkspaceRoot) --
// unlike every other activity in this package. This is a real deployment
// constraint the interim implementation carries forward, not hidden: if
// either is unconfigured, ResolvePlan fails fast with a clear error rather
// than silently degrading (see Activities.ResolvePlan below).
package release

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"go.temporal.io/sdk/activity"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// planCLIResult is the subset of tools/release_helper_go/cmd/plan.go's
// PlanResult this activity needs, decoded from `release_helper_go plan
// --format=json`'s stdout. Deliberately not importing cmd.PlanResult
// itself: this package should not depend on release_helper_go/cmd's
// (CLI-oriented, cobra-flag-heavy) internals just for a struct shape --
// keeping this local, minimal, and positionally coupled only to the JSON
// field names is the safer interim boundary. A future library extraction
// (see this file's package doc comment) is the natural point to replace
// this with a real shared type.
type planCLIResult struct {
	Versions map[string]string `json:"versions"`
}

// ResolvePlan implements ReleaseActivities.ResolvePlan for real: it
// resolves versions once for the whole batch by invoking `release_helper_go
// plan --format=json --apps=<image targets> --charts=<chart targets>
// --increment-patch`, using the activity's own Temporal WorkflowExecution
// id (deterministic per batch -- see WorkflowID's doc comment) as the
// idempotency-key-prefix so retries of this activity (Temporal's
// at-least-once activity execution, NFR3) hit the same App Registry
// version-allocation idempotency key rather than allocating a second
// version on redelivery -- see planParams.idempotencyPrefix and
// AllocateVersion's own idempotency contract (unchanged by this task).
//
// requested app/chart names are targets[].OwnerFullName verbatim: the CLI's
// resolveApps (plan.go) matches requested names against AppMetadata.
// FullName() first, which is exactly OwnerFullName's format -- see
// github.go's splitPlanTargets doc comment.
func (a *Activities) ResolvePlan(ctx context.Context, targets []ReleaseTarget) (ResolvedPlan, error) {
	if len(targets) == 0 {
		return ResolvedPlan{}, fmt.Errorf("resolve plan: no targets")
	}
	if a.WorkspaceRoot == "" {
		return ResolvedPlan{}, fmt.Errorf("resolve plan: WorkspaceRoot not configured -- ResolvePlan's interim shell-out implementation requires a full monorepo checkout (see plan.go's package doc comment)")
	}

	var appOwners, chartOwners []string
	for _, t := range targets {
		switch t.Kind {
		case repository.ArtifactKindImage:
			appOwners = append(appOwners, t.OwnerFullName)
		case repository.ArtifactKindChart:
			chartOwners = append(chartOwners, t.OwnerFullName)
		default:
			return ResolvedPlan{}, fmt.Errorf("resolve plan: unsupported target kind %q for %s", t.Kind, t.OwnerFullName)
		}
	}

	args := []string{"plan", "--format=json", "--event-type=workflow_dispatch", "--increment-patch"}
	if len(appOwners) > 0 {
		args = append(args, "--apps="+strings.Join(appOwners, ","))
	}
	if len(chartOwners) > 0 {
		args = append(args, "--charts="+strings.Join(chartOwners, ","))
	}
	if info := activity.GetInfo(ctx); info.WorkflowExecution.ID != "" {
		args = append(args, "--idempotency-key-prefix="+info.WorkflowExecution.ID)
	}

	bin := a.PlanBinaryPath
	if bin == "" {
		bin = "release_helper_go"
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = a.WorkspaceRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return ResolvedPlan{}, fmt.Errorf("resolve plan: %s plan: %w: %s", bin, err, strings.TrimSpace(stderr.String()))
	}

	var parsed planCLIResult
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return ResolvedPlan{}, fmt.Errorf("resolve plan: parse plan output: %w", err)
	}

	versions := make(map[string]string, len(targets))
	for _, t := range targets {
		v, ok := parsed.Versions[t.OwnerFullName]
		if !ok || v == "" {
			return ResolvedPlan{}, fmt.Errorf("resolve plan: no version resolved for target %s (plan output had no entry for %q)", t.key(), t.OwnerFullName)
		}
		versions[t.key()] = v
	}

	return ResolvedPlan{Versions: versions, RawJSON: stdout.Bytes()}, nil
}
