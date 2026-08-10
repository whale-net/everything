package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// newManifestSetCmd discovers every releasable app and helm chart manifest
// and emits them as a single appmetapb.AppManifestSet. This is the input
// AR-2c's registry recording step passes to AppRegistry.ReconcileApps: no
// conversion code is needed because ListAllApps/ListAllHelmCharts already
// decode into appmetapb types (see //tools/appmeta/README.md, AR-M).
func newManifestSetCmd() *cobra.Command {
	var gitSHA string

	cmd := &cobra.Command{
		Use:   "manifest-set",
		Short: "Emit the full discovered app/chart manifest set as an AppManifestSet",
		Long: `Discovers every releasable app_metadata and helm_chart_metadata target via
the same bazel query/cquery discovery ListAllApps and ListAllHelmCharts use
for plan/plan-helm-release — which already excludes testonly fixtures — and
emits the result as an appmetapb.AppManifestSet, marshaled with protojson so
enums (e.g. deploy_unit) serialize as names rather than integers.

This is what AR-2c pipes to AppRegistry.ReconcileApps: a full-replace set, so
callers must not filter it before sending.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			apps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}
			charts, err := ListAllHelmCharts(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}

			sha := strings.TrimSpace(gitSHA)
			if sha == "" {
				sha, err = defaultGit.Run("rev-parse", "HEAD")
				if err != nil {
					return fmt.Errorf("resolve git sha (pass --git-sha to override): %w", err)
				}
				sha = strings.TrimSpace(sha)
			}

			set := &appmetapb.AppManifestSet{
				GitSha:            sha,
				DiscoveredAt:      time.Now().Unix(),
				SourceCommittedAt: resolveSourceCommittedAt(defaultGit, sha),
			}
			for i := range apps {
				set.Apps = append(set.Apps, apps[i].AppManifest)
			}
			for i := range charts {
				set.Charts = append(set.Charts, charts[i].ChartManifest)
			}

			out, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(set)
			if err != nil {
				return fmt.Errorf("marshal manifest set: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}

	cmd.Flags().StringVar(&gitSHA, "git-sha", "", "Commit SHA to record (default: git rev-parse HEAD)")
	return cmd
}

// resolveSourceCommittedAt returns the git committer timestamp (Unix
// seconds) of sha, for AppManifestSet.source_committed_at -- the app
// registry's reconcile-watermark ordering key (see appmeta.proto's doc
// comment on that field and issue #545). Unlike git_sha resolution above,
// a failure here does NOT fail the command: this field is a correctness
// improvement over discovered_at, not a hard requirement, and the server
// falls back to discovered_at when it's 0 (e.g. git unavailable, or sha
// unresolvable -- a shallow clone missing the commit object). Losing the
// stronger ordering guarantee for one call is far better than breaking
// manifest-set entirely.
func resolveSourceCommittedAt(git GitRunner, sha string) int64 {
	out, err := git.Run("log", "-1", "--format=%ct", sha)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
