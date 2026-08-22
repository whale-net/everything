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
// # bazel/WorkspaceRoot is NOT required for this activity's real call path
//
// A prod run with WorkspaceRoot unset used to fail here immediately
// ("WorkspaceRoot not configured"). Tracing what ResolvePlan's actual
// invocation of `release_helper_go plan` needs turned up that the bazel
// dependency was pure re-derivation: ListAllApps/ListAllHelmCharts
// (tools/release_helper_go/cmd/metadata.go, plan_helm.go) run `bazel
// query`/`cquery` purely to *discover* AppMetadata/HelmChartMetadata for a
// caller-supplied name list -- but this activity's caller (the App
// Registry itself, via ReleaseTarget) already knows exactly which
// Domain/Name/AppType each target has, and this activity only ever reads
// the plan's `versions` map back out of the CLI's JSON stdout (see below).
// ResolvePlan therefore looks up each target's App/Chart row via a.Registry
// (the same direct-Postgres registry AssertApps/BeginPublishBatch use
// elsewhere) and passes that as `--apps-metadata`/`--charts-metadata` JSON
// -- release_helper_go's bazel-free path (AppMetadataFromInputs/
// HelmChartMetadataFromInputs) -- instead of `--apps=<names>`. The only
// remaining use of `git` in that path is assignVersions' fallback closure
// (a *read*, `git tag --list`), reached only if AllocateVersion returns
// FailedPrecondition -- rare, and harmless in a scratch directory with no
// push credentials. WorkspaceRoot is therefore optional here: when unset,
// ResolvePlan uses a fresh os.MkdirTemp per invocation as cmd.Dir, which
// also removes the git-tag-race concurrency caveat a single shared
// WorkspaceRoot directory used to carry (each invocation now gets its own
// directory). FinalizePublish (finalize.go) has a materially different
// requirement -- see that file's package doc comment.
package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

// appMetadataInput/chartMetadataInput mirror
// tools/release_helper_go/cmd's AppMetadataInput/HelmChartMetadataInput
// JSON shape exactly (field names and json tags) -- same "local, minimal,
// positionally coupled" boundary as planCLIResult above, not an import of
// the CLI-oriented package.
type appMetadataInput struct {
	Domain  string `json:"domain"`
	Name    string `json:"name"`
	AppType string `json:"app_type"`
}

type chartMetadataInput struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
}

// ResolvePlan implements ReleaseActivities.ResolvePlan for real: it
// resolves versions once for the whole batch by invoking `release_helper_go
// plan --format=json --apps-metadata=<image targets> --charts-metadata=<chart
// targets> --increment-patch`, using the activity's own Temporal
// WorkflowExecution id (deterministic per batch -- see WorkflowID's doc
// comment) as the idempotency-key-prefix so retries of this activity
// (Temporal's at-least-once activity execution, NFR3) hit the same App
// Registry version-allocation idempotency key rather than allocating a
// second version on redelivery -- see planParams.idempotencyPrefix and
// AllocateVersion's own idempotency contract (unchanged by this task).
//
// Each target's Domain/Name/AppType is looked up via a.Registry (the App
// Registry's own App/Chart rows, already keyed by OwnerFullName) rather
// than resolved by `release_helper_go`'s bazel-query discovery -- see this
// file's package doc comment for why that bazel dependency was redundant
// here.
func (a *Activities) ResolvePlan(ctx context.Context, targets []ReleaseTarget) (ResolvedPlan, error) {
	if len(targets) == 0 {
		return ResolvedPlan{}, fmt.Errorf("resolve plan: no targets")
	}

	var appsMetadata []appMetadataInput
	var chartsMetadata []chartMetadataInput
	for _, t := range targets {
		switch t.Kind {
		case repository.ArtifactKindImage:
			app, err := a.Registry.Apps().GetAppByFullName(ctx, t.OwnerFullName)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return ResolvedPlan{}, fmt.Errorf("resolve plan: no App Registry app named %q", t.OwnerFullName)
				}
				return ResolvedPlan{}, fmt.Errorf("resolve plan: look up app %q: %w", t.OwnerFullName, err)
			}
			appsMetadata = append(appsMetadata, appMetadataInput{Domain: app.Domain, Name: app.Name, AppType: app.AppType})
		case repository.ArtifactKindChart:
			chart, err := a.Registry.Apps().GetChartByFullName(ctx, t.OwnerFullName)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return ResolvedPlan{}, fmt.Errorf("resolve plan: no App Registry chart named %q", t.OwnerFullName)
				}
				return ResolvedPlan{}, fmt.Errorf("resolve plan: look up chart %q: %w", t.OwnerFullName, err)
			}
			chartsMetadata = append(chartsMetadata, chartMetadataInput{Domain: chart.Domain, Name: chart.Name})
		default:
			return ResolvedPlan{}, fmt.Errorf("resolve plan: unsupported target kind %q for %s", t.Kind, t.OwnerFullName)
		}
	}

	// versionSelections carries each target's own per-target version choice
	// (issue #889 follow-up: the release-trigger UI's Draft-page picker) as
	// release_helper_go's --version-selections JSON, keyed by OwnerFullName.
	// A target with an empty VersionSelection (the pre-picker default, and
	// every target on the old free-form-scope path) is simply omitted --
	// when the whole batch omits it, --increment-patch below still supplies
	// the batch-wide default unchanged.
	versionSelections := map[string]string{}
	for _, t := range targets {
		if t.VersionSelection != "" {
			versionSelections[t.OwnerFullName] = t.VersionSelection
		}
	}

	args := []string{"plan", "--format=json", "--event-type=workflow_dispatch"}
	if len(versionSelections) < len(targets) {
		// At least one target has no per-target selection -- --increment-patch
		// remains the batch-wide default for it (and for every target, when
		// versionSelections is empty entirely).
		args = append(args, "--increment-patch")
	}
	if len(versionSelections) > 0 {
		raw, err := json.Marshal(versionSelections)
		if err != nil {
			return ResolvedPlan{}, fmt.Errorf("resolve plan: marshal version selections: %w", err)
		}
		args = append(args, "--version-selections="+string(raw))
	}
	if len(appsMetadata) > 0 {
		raw, err := json.Marshal(appsMetadata)
		if err != nil {
			return ResolvedPlan{}, fmt.Errorf("resolve plan: marshal apps metadata: %w", err)
		}
		args = append(args, "--apps-metadata="+string(raw))
	}
	if len(chartsMetadata) > 0 {
		raw, err := json.Marshal(chartsMetadata)
		if err != nil {
			return ResolvedPlan{}, fmt.Errorf("resolve plan: marshal charts metadata: %w", err)
		}
		args = append(args, "--charts-metadata="+string(raw))
	}
	if info := activity.GetInfo(ctx); info.WorkflowExecution.ID != "" {
		args = append(args, "--idempotency-key-prefix="+info.WorkflowExecution.ID)
	}

	bin := a.PlanBinaryPath
	if bin == "" {
		bin = "release_helper_go"
	}

	// WorkspaceRoot is optional for this activity (see package doc
	// comment): the bazel-free --apps-metadata/--charts-metadata path
	// needs no full monorepo checkout, only a writable cwd for git's rare
	// tag-fallback read. Default to a fresh scratch directory per
	// invocation rather than requiring a pre-provisioned one.
	dir := a.WorkspaceRoot
	if dir == "" {
		tmp, err := os.MkdirTemp("", "release-plan-*")
		if err != nil {
			return ResolvedPlan{}, fmt.Errorf("resolve plan: create scratch workspace: %w", err)
		}
		defer os.RemoveAll(tmp) //nolint:errcheck
		dir = tmp
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
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
