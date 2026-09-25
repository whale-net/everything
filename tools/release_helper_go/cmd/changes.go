package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newChangesCmd() *cobra.Command {
	var baseCommit string

	cmd := &cobra.Command{
		Use:          "changes",
		Short:        "Detect changed apps since a commit",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}
			if baseCommit == "" {
				prev, err := getPreviousTag(defaultGit)
				if err == nil && prev != "" {
					baseCommit = prev
					fmt.Fprintf(cmd.ErrOrStderr(), "Auto-detected previous tag: %s\n", baseCommit)
				}
			}
			if baseCommit != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Detecting changes against commit: %s\n", baseCommit)
			} else {
				fmt.Fprintln(cmd.ErrOrStderr(), "No base commit specified, considering all apps as changed")
			}
			apps, err := DetectChangedApps(baseCommit, defaultBazel, defaultGit, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}
			for _, app := range apps {
				fmt.Fprintln(cmd.OutOrStdout(), app.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&baseCommit, "base-commit", "", "Compare changes against this commit")
	return cmd
}

func newChangedTargetsCmd() *cobra.Command {
	var baseCommit string
	var candidates string

	cmd := &cobra.Command{
		Use:   "changed-targets",
		Short: "Filter a pool of candidate Bazel targets down to those affected by changes since a commit",
		Long: "Given a Bazel query expression describing a pool of candidate targets (e.g. a family of\n" +
			"integration tests), prints the subset of those targets affected by file changes since\n" +
			"--base-commit. If --base-commit is empty, or the diff touches global build configuration\n" +
			"that rdeps cannot attribute (MODULE.bazel, .bazelrc, WORKSPACE, .bazelversion), every\n" +
			"candidate target is printed -- callers should treat that as \"run everything\", not as\n" +
			"\"nothing changed\". Changes to .bzl or .lock files are scoped precisely rather than\n" +
			"treated as global.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if candidates == "" {
				return fmt.Errorf("--candidates is required")
			}
			if baseCommit != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Detecting changed targets against commit: %s\n", baseCommit)
			} else {
				fmt.Fprintln(cmd.ErrOrStderr(), "No base commit specified, considering all candidate targets as affected")
			}
			targets, err := DetectAffectedTargets(baseCommit, candidates, defaultBazel, defaultGit)
			if err != nil {
				return err
			}
			for _, t := range targets {
				fmt.Fprintln(cmd.OutOrStdout(), t)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&baseCommit, "base-commit", "", "Compare changes against this commit; if empty, all candidates are considered affected")
	cmd.Flags().StringVar(&candidates, "candidates", "", "Bazel query expression for the pool of candidate targets to filter (required)")
	return cmd
}

// DetectChangedApps finds apps affected by changes since baseCommit.
// If baseCommit is empty, all apps are considered changed.
func DetectChangedApps(baseCommit string, bazel BazelRunner, git GitRunner, fs FileSystem, workspaceRoot string) ([]AppMetadata, error) {
	allApps, err := ListAllApps(bazel, fs, workspaceRoot)
	if err != nil {
		return nil, err
	}
	if baseCommit == "" {
		return allApps, nil
	}

	changedFiles, err := getChangedFiles(baseCommit, git)
	if err != nil {
		return nil, err
	}
	if len(changedFiles) == 0 {
		return nil, nil
	}

	// If workspace root build configuration or shared Starlark macros change,
	// all apps are affected.
	if hasGlobalBuildChanges(changedFiles) {
		return allApps, nil
	}

	relevant := filterBuildFiles(changedFiles)
	if len(relevant) == 0 {
		return nil, nil
	}

	fileLabels, changedPkgs := filesToBazelLabels(relevant)

	// Validate labels (remove deleted files / invalid targets)
	validLabels := validateLabels(fileLabels, bazel)
	// Validate packages (remove packages whose BUILD file was deleted, e.g. a removed directory)
	validPkgs := validatePackages(changedPkgs, bazel)

	if len(validLabels) == 0 && len(validPkgs) == 0 {
		return nil, nil
	}

	expr := unionQueryExpr(validLabels, validPkgs)

	// Find which app_metadata targets are affected by the changed files.
	metaTargets := make([]string, 0, len(allApps))
	for _, app := range allApps {
		metaTargets = append(metaTargets, app.BazelTarget)
	}
	if len(metaTargets) == 0 {
		return nil, nil
	}

	metaExpr := strings.Join(metaTargets, " + ")
	if len(metaTargets) > 1 {
		metaExpr = "(" + metaExpr + ")"
	}
	// `--keep_going` so a broken transitive closure elsewhere in `//...`
	// (e.g. a stale Go module reference) doesn't prevent us from learning
	// which metadata targets the diff actually touches. Bazel exits non-zero
	// in that case but still prints any matched labels to stdout, plus a
	// `WARNING: Results may be inaccurate` line on stderr. We surface the
	// warning and continue — for change detection, treating "unknown" as
	// "no apps affected" is the safe direction: PR images aren't pushed,
	// and the main-branch path runs again on push with a fresh universe.
	rdepsExpr := fmt.Sprintf("rdeps(%s, %s)", metaExpr, expr)
	affectedMetaOut, rdepsErr := bazel.Run("query", rdepsExpr, "--output=label", "--keep_going")
	if rdepsErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: bazel rdeps query returned non-zero (%v); continuing with partial results\n", rdepsErr)
	}
	affectedMeta := labelSet(affectedMetaOut)

	var result []AppMetadata
	for _, app := range allApps {
		if affectedMeta[app.BazelTarget] {
			result = append(result, app)
		}
	}
	return result, nil
}

// unionQueryExpr builds a Bazel query union expression from changed-file
// labels and changed packages, wrapping multi-part unions in parens.
func unionQueryExpr(labels []string, pkgs []string) string {
	queryParts := make([]string, 0, len(labels)+len(pkgs))
	if len(labels) > 0 {
		queryParts = append(queryParts, strings.Join(labels, " + "))
	}
	for _, pkg := range pkgs {
		if pkg == "//" {
			queryParts = append(queryParts, "//...")
		} else {
			queryParts = append(queryParts, pkg+"/...")
		}
	}
	expr := strings.Join(queryParts, " + ")
	if len(queryParts) > 1 {
		expr = "(" + expr + ")"
	}
	return expr
}

// DetectAffectedTargets filters candidatesExpr (an arbitrary Bazel query
// expression describing a pool of targets, e.g. a family of integration
// tests) down to the subset affected by changes since baseCommit.
//
// If baseCommit is empty, or the diff touches global build configuration that
// rdeps cannot attribute (MODULE.bazel, .bazelrc, WORKSPACE, .bazelversion),
// every target matching candidatesExpr is returned -- callers should treat
// that as "run everything". Unlike the release path (DetectChangedApps), this
// scopes .bzl and .lock edits precisely instead of treating them as global,
// because here a false positive costs a slow full-pool container test run.
func DetectAffectedTargets(baseCommit string, candidatesExpr string, bazel BazelRunner, git GitRunner) ([]string, error) {
	if baseCommit == "" {
		return queryLabels(bazel, candidatesExpr)
	}

	changedFiles, err := getChangedFiles(baseCommit, git)
	if err != nil {
		return nil, err
	}
	if len(changedFiles) == 0 {
		return nil, nil
	}

	if hasUnscopableChanges(changedFiles) {
		return queryLabels(bazel, candidatesExpr)
	}

	relevant := filterBuildFiles(changedFiles)
	if len(relevant) == 0 {
		return nil, nil
	}

	fileLabels, changedPkgs := filesToBazelLabels(relevant)
	validLabels := validateLabels(fileLabels, bazel)
	validPkgs := validatePackages(changedPkgs, bazel)
	if len(validLabels) == 0 && len(validPkgs) == 0 {
		return nil, nil
	}

	changedExpr := unionQueryExpr(validLabels, validPkgs)

	// rdeps(universe, x) returns every node in the transitive closure of
	// `universe` that depends on x -- not just members of `universe` itself.
	// For a leaf-shaped universe like DetectChangedApps' app_metadata targets
	// (nothing else in the graph depends on them) that coincides with what we
	// want, but candidatesExpr here is caller-supplied and may denote targets
	// with intermediate library/source-file dependents between them and the
	// changed files (e.g. `tests(//...)`): rdeps would then also surface
	// those intermediate, non-candidate nodes. Intersecting back with
	// candidatesExpr restricts the result to actual candidates.
	//
	// Same --keep_going rationale as DetectChangedApps: a broken transitive
	// closure elsewhere shouldn't prevent learning which candidates the diff
	// touches, and "unknown" collapsing to "no targets affected" is the safe
	// direction here -- the caller's fallback (unfiltered run on push-to-main)
	// still covers it.
	rdepsExpr := fmt.Sprintf("(%s) intersect rdeps(%s, %s)", candidatesExpr, candidatesExpr, changedExpr)
	out, rdepsErr := bazel.Run("query", rdepsExpr, "--output=label", "--keep_going")
	if rdepsErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: bazel rdeps query returned non-zero (%v); continuing with partial results\n", rdepsErr)
	}
	return splitNonEmpty(out), nil
}

// queryLabels runs a plain label query, returning every matching target.
func queryLabels(bazel BazelRunner, expr string) ([]string, error) {
	out, err := bazel.Run("query", expr, "--output=label", "--keep_going")
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("bazel query: %w", err)
	}
	return splitNonEmpty(out), nil
}

func getChangedFiles(baseCommit string, git GitRunner) ([]string, error) {
	out, err := git.Run("diff", "--name-only", baseCommit+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	var files []string
	for _, f := range strings.Split(out, "\n") {
		f = strings.TrimSpace(f)
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

func getPreviousTag(git GitRunner) (string, error) {
	out, err := git.Run("describe", "--tags", "--abbrev=0", "HEAD^")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// hasGlobalBuildChanges reports whether the changed files include workspace-level build
// configuration or a shared macro whose effect cannot be attributed to a specific set of
// targets -- i.e. every app is a candidate. This is the conservative release-path check
// used by DetectChangedApps: under-building a release ships a stale image, so it errs
// toward "rebuild everything" for .bzl and .lock edits.
func hasGlobalBuildChanges(files []string) bool {
	for _, f := range files {
		switch f {
		case "MODULE.bazel", "MODULE.bazel.lock",
			"WORKSPACE", "WORKSPACE.bzlmod", "WORKSPACE.bazel", ".bazelrc", ".bazelversion":
			return true
		}
		if strings.HasSuffix(f, ".bzl") || strings.HasSuffix(f, ".lock") {
			return true
		}
	}
	return false
}

// hasUnscopableChanges is the narrower global check DetectAffectedTargets uses before it
// tries to scope a candidate pool with rdeps. It matches hasGlobalBuildChanges minus .bzl
// and .lock, because rdeps attributes both precisely: a .bzl file is an ordinary source
// target in the Bazel graph, so every candidate whose BUILD loads it has a real reverse-
// dependency edge to it, and a .lock file is not a build input at all. Treating either as
// global meant a one-macro edit (or a regenerated lockfile) re-ran an entire candidate pool
// -- e.g. the full container-backed DB suite -- for no correctness benefit, making a
// narrowly-scoped PR as slow as a whole-repo change.
func hasUnscopableChanges(files []string) bool {
	for _, f := range files {
		switch f {
		case "MODULE.bazel",
			"WORKSPACE", "WORKSPACE.bzlmod", "WORKSPACE.bazel", ".bazelrc", ".bazelversion":
			return true
		}
	}
	return false
}

// filterBuildFiles removes files that cannot affect any build (docs, CI, auto-generated
// lockfiles, etc.). It deliberately keeps .bzl files: they are real source files in the
// Bazel graph (loaded by the packages that reference them), so filesToBazelLabels turns
// them into labels and the rdeps intersection scopes the affected targets precisely.
func filterBuildFiles(files []string) []string {
	var out []string
	for _, f := range files {
		if strings.HasPrefix(f, ".github/workflows/") ||
			strings.HasPrefix(f, ".github/actions/") ||
			strings.HasPrefix(f, "docs/") ||
			strings.HasSuffix(f, ".md") ||
			strings.HasSuffix(f, "copilot-instructions.md") ||
			strings.HasSuffix(f, ".lock") {
			continue
		}
		switch f {
		case "MODULE.bazel",
			"WORKSPACE", "WORKSPACE.bzlmod", "WORKSPACE.bazel", ".bazelrc", ".bazelversion":
			continue
		}
		if strings.HasPrefix(f, ".bazel") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// filesToBazelLabels converts git file paths to Bazel labels and package sets.
func filesToBazelLabels(files []string) (labels []string, packages map[string]struct{}) {
	packages = make(map[string]struct{})
	for _, f := range files {
		base := filepath.Base(f)
		if base == "BUILD" || base == "BUILD.bazel" {
			dir := filepath.Dir(f)
			if dir == "." {
				packages["//"] = struct{}{}
			} else {
				packages["//"+dir] = struct{}{}
			}
			continue
		}
		parts := strings.SplitN(f, "/", 2)
		if len(parts) == 1 {
			labels = append(labels, "//:"+f)
		} else {
			dir := filepath.Dir(f)
			labels = append(labels, "//"+dir+":"+filepath.Base(f))
		}
	}
	return labels, packages
}

// validateLabels filters labels to those Bazel can resolve (removes deleted files etc.).
func validateLabels(labels []string, bazel BazelRunner) []string {
	if len(labels) == 0 {
		return nil
	}
	// Try batch query with --keep_going first
	expr := strings.Join(labels, " + ")
	out, err := bazel.Run("query", expr, "--output=label", "--keep_going")
	if err == nil {
		return splitNonEmpty(out)
	}

	// If batch failed with non-zero exit code, keep_going may still have emitted valid targets
	if valid := splitNonEmpty(out); len(valid) > 0 {
		return valid
	}

	// Fall back to individual validation only if nothing was parsed
	var valid []string
	for _, label := range labels {
		if out, err := bazel.Run("query", label, "--output=label"); err == nil {
			if t := strings.TrimSpace(out); t != "" {
				valid = append(valid, t)
			}
		}
	}
	return valid
}

// validatePackages drops packages that no longer exist (e.g. a deleted
// directory whose BUILD file was among the changed files).
func validatePackages(packages map[string]struct{}, bazel BazelRunner) []string {
	if len(packages) == 0 {
		return nil
	}
	var pkgList []string
	var queryExprs []string
	for pkg := range packages {
		pkgList = append(pkgList, pkg)
		if pkg == "//" {
			queryExprs = append(queryExprs, "//...")
		} else {
			queryExprs = append(queryExprs, pkg+"/...")
		}
	}

	// Try batch query with --keep_going first
	expr := strings.Join(queryExprs, " + ")
	if out, err := bazel.Run("query", expr, "--output=package", "--keep_going"); err == nil || out != "" {
		validSet := labelSet(out)
		var valid []string
		for _, pkg := range pkgList {
			cleanPkg := strings.TrimPrefix(pkg, "//")
			if cleanPkg == "" {
				cleanPkg = "//"
			}
			if validSet[cleanPkg] || validSet[pkg] {
				valid = append(valid, pkg)
			}
		}
		if len(valid) > 0 {
			return valid
		}
	}

	// Fall back to individual validation if batch didn't return any packages
	var valid []string
	for _, pkg := range pkgList {
		expr := pkg + "/..."
		if pkg == "//" {
			expr = "//..."
		}
		if out, err := bazel.Run("query", expr, "--output=label"); err == nil {
			if strings.TrimSpace(out) != "" {
				valid = append(valid, pkg)
			}
		}
	}
	return valid
}

// labelSet converts newline-separated label output to a set.
func labelSet(out string) map[string]bool {
	set := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			set[t] = true
		}
	}
	return set
}
