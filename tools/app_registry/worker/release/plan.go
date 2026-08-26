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
// WorkflowExecution *run* id -- NOT the workflow id -- as the
// idempotency-key-prefix so retries of this activity (Temporal's
// at-least-once activity execution, NFR3) hit the same App Registry
// version-allocation idempotency key rather than allocating a second
// version on redelivery -- see planParams.idempotencyPrefix and
// AllocateVersion's own idempotency contract (unchanged by this task).
//
// This must be RunID, not WorkflowID: WorkflowID (WorkflowID's doc comment)
// is deliberately deterministic per target batch -- the same "release
// tools domain" request always hashes to the same WorkflowID, by design,
// so Temporal's own WorkflowExecutionAlreadyStarted rejection can enforce
// "at most one non-terminal release per target". But that means WorkflowID
// is reused across every genuinely distinct trigger of the same batch
// (each gets a fresh execution -- and a fresh RunID -- once the prior one
// reaches a terminal state), while at-least-once activity redelivery
// happens *within* a single execution, which always keeps the same RunID.
// Keying on WorkflowID instead of RunID (the bug, confirmed against prod:
// idempotency_key row
// "release-5ac1b1d5...-tools-app-registry-allocate", created once at
// 2026-08-23T03:20:59Z) makes every later trigger of "release tools
// domain" replay that first execution's cached AllocateVersion response
// forever -- silently returning a stale version (v0.5.0) instead of the
// real next one, no matter how many hours or how many other successful
// releases of the same targets happened via other paths in between. RunID
// still correctly dedupes true same-execution activity retries (the
// property this idempotency key exists for) while giving each new
// execution of the same batch its own key.
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
	targetDomain := make(map[string]string, len(targets))
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
			targetDomain[t.OwnerFullName] = app.Domain
		case repository.ArtifactKindChart:
			chart, err := a.Registry.Apps().GetChartByFullName(ctx, t.OwnerFullName)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return ResolvedPlan{}, fmt.Errorf("resolve plan: no App Registry chart named %q", t.OwnerFullName)
				}
				return ResolvedPlan{}, fmt.Errorf("resolve plan: look up chart %q: %w", t.OwnerFullName, err)
			}
			chartsMetadata = append(chartsMetadata, chartMetadataInput{Domain: chart.Domain, Name: chart.Name})
			targetDomain[t.OwnerFullName] = chart.Domain
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

	// Fail fast, with a specific and actionable message, for any target
	// that will deterministically hit the "workspace root not found"
	// failure below instead of letting it happen (issue: a real prod
	// release run -- 9a1d783c-3cac-44fe-87d0-0d38793c43f8, domain
	// manmanv2 -- failed exactly this way for all 7 of its targets).
	//
	// A target with no explicit VersionSelection resolves through
	// release_helper_go's resolveVersion (registry_version.go), which
	// calls AllocateVersion first and only falls back to the git-tag
	// autoIncrementVersion (plan.go) if AllocateVersion returns
	// FailedPrecondition -- which it always does for a domain not yet at
	// App Registry adoption stage "allocate" (server/handlers/artifact.go).
	// That fallback needs a real git checkout to list tags from; this
	// activity only has one when a.WorkspaceRoot is explicitly configured
	// (see this file's package doc comment on why that's optional and
	// normally unset in prod). So: no WorkspaceRoot + any domain not at
	// "allocate" + no explicit version for that domain's targets is not a
	// rare edge case here, it is a guaranteed failure -- and worth
	// diagnosing plainly rather than via a leaf-level git error that names
	// a `/tmp` path nobody can act on.
	if a.WorkspaceRoot == "" {
		seen := make(map[string]bool)
		var blocked []string
		for _, t := range targets {
			if t.VersionSelection != "" {
				continue // an explicit version never reaches the git fallback
			}
			domain := targetDomain[t.OwnerFullName]
			if seen[domain] {
				continue
			}
			seen[domain] = true
			stage, err := a.Registry.DomainAdoption().GetStage(ctx, domain)
			if err != nil {
				return ResolvedPlan{}, fmt.Errorf("resolve plan: check App Registry adoption stage for domain %q: %w", domain, err)
			}
			if stage != repository.DomainAdoptionStageAllocate {
				blocked = append(blocked, domain)
			}
		}
		if len(blocked) > 0 {
			return ResolvedPlan{}, fmt.Errorf(
				"resolve plan: cannot auto-increment a version for domain(s) %s: not yet at App Registry adoption stage %q, and this activity has no git checkout for the pre-adoption git-tag fallback to read tags from -- either supply an explicit version for every target in %s, or complete that domain's App Registry cutover first",
				strings.Join(blocked, ", "), repository.DomainAdoptionStageAllocate, strings.Join(blocked, ", "),
			)
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
	if info := activity.GetInfo(ctx); info.WorkflowExecution.RunID != "" {
		args = append(args, "--idempotency-key-prefix="+info.WorkflowExecution.RunID)
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
	// registryOptedIn() (release_helper_go/cmd/plan.go) requires
	// APP_REGISTRY_CICD_OPT_IN=true to call AllocateVersion at all --
	// without it, resolveVersion's client stays nil and every version
	// resolution silently takes the git-tag tagFallback path instead,
	// which is always broken here (this activity's cwd is the bare
	// scratch `dir` above, with no .git -- see this file's package doc
	// comment). APP_REGISTRY_CICD_OPT_IN is documented as a GitHub
	// Actions repository variable (DEPLOY.md/ENV.md) with no equivalent
	// ever set on this worker's own process env (unlike
	// APP_REGISTRY_ADDRESS/GRPC_AUTH_* below, which main.go already sets
	// for the writeback stub's own gRPC client and this subprocess
	// inherits via os.Environ()) -- so registryOptedIn() was always false
	// here regardless of the target domain's adoption stage or its
	// actual App Registry version history, and every batch silently
	// resolved to v0.0.1. This activity's whole reason to exist is to
	// call the App Registry-backed release path, so force it on
	// unconditionally rather than depending on a CI-oriented repository
	// variable that was never wired to this deployment.
	cmd.Env = append(os.Environ(), "APP_REGISTRY_CICD_OPT_IN=true")
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
	kinds := make(map[string]string, len(targets))
	for _, t := range targets {
		v, ok := parsed.Versions[t.OwnerFullName]
		if !ok || v == "" {
			return ResolvedPlan{}, fmt.Errorf("resolve plan: no version resolved for target %s (plan output had no entry for %q)", t.key(), t.OwnerFullName)
		}
		versions[t.key()] = v
		
		// Populate kind from the app/chart metadata that was looked up
		// Apps are keyed by OwnerFullName (domain-name format) in appsMetadata
		// Charts are similar in chartsMetadata
		kind := "ARTIFACT_KIND_UNSPECIFIED"
		for _, am := range appsMetadata {
			if am.Domain+"-"+am.Name == t.OwnerFullName {
				// Determine kind from app_type
				switch am.AppType {
				case "cli", "binary":
					kind = string(repository.ArtifactKindBinary)
				case "firmware":
					kind = string(repository.ArtifactKindFirmware)
				default:
					kind = string(repository.ArtifactKindImage)
				}
				break
			}
		}
		if kind == "ARTIFACT_KIND_UNSPECIFIED" {
			for _, cm := range chartsMetadata {
				if cm.Domain+"-"+cm.Name == t.OwnerFullName {
					kind = string(repository.ArtifactKindChart)
					break
				}
			}
		}
		kinds[t.key()] = kind
	}

	return ResolvedPlan{Versions: versions, Kinds: kinds, RawJSON: stdout.Bytes()}, nil
}
