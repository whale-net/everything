package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/whale-net/everything/tools/app_registry/apierrors"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
)

var (
	serverAddr string
	format     string
)

var rootCmd = &cobra.Command{
	Use:          "app-registry",
	Short:        "Thin gRPC client for the App Registry",
	Long:         "app-registry is a thin gRPC client for the App Registry. It validates argument shape, calls one RPC, and formats the response — no business logic lives here.",
	SilenceUsage: true,
}

// exitOwnerNotReconciled is the process exit code Execute uses for a
// RecordArtifact call that failed because owner_full_name isn't registered
// yet (see apierrors.ReasonOwnerNotReconciled) -- issue #547. This lets a CI
// caller (.github/actions/app-registry-record-image/action.yml and
// release.yml's inline chart-recording loop) branch on `$?` to print an
// actionable "app isn't registered yet, re-run after main's CI" annotation,
// distinct from every other failure (registry outage, auth, timeout), which
// still exits 1. Chosen over string-matching stderr because the reason is
// carried as a structured gRPC status detail (see errorInfoReason below),
// not parsed from the human message -- more robust, and testable with a Go
// unit test (see root_test.go) rather than only at the shell-script layer.
//
// Keep this constant's value in sync with the two GitHub Actions call
// sites above if it ever changes -- they check for exit code 3 literally,
// since a shell script can't import this package.
const exitOwnerNotReconciled = 3

// Execute runs the CLI. Called from main.go.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCodeFor(err))
	}
}

// exitCodeFor classifies err into a process exit code. Every failure other
// than the owner-not-reconciled case keeps the CLI's long-standing exit
// code 1 -- this only carves out one specific, structured signal a caller
// can rely on; it is not a general error-taxonomy exit-code scheme.
func exitCodeFor(err error) int {
	if isOwnerNotReconciled(err) {
		return exitOwnerNotReconciled
	}
	return 1
}

// isOwnerNotReconciled reports whether err is a gRPC status carrying an
// errdetails.ErrorInfo with Reason == apierrors.ReasonOwnerNotReconciled
// (set server-side by server/handlers/errors.go's mapRepoErr). A non-gRPC
// error (e.g. a flag-validation failure that never reached the server)
// naturally reports false.
func isOwnerNotReconciled(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.Reason == apierrors.ReasonOwnerNotReconciled {
			return true
		}
	}
	return false
}

func init() {
	rootCmd.SilenceErrors = true
	rootCmd.PersistentFlags().StringVar(&serverAddr, "address", defaultServerAddr(), "app-registry-api address (host:port)")
	rootCmd.PersistentFlags().StringVar(&format, "format", "json", "Output format: json or table")

	rootCmd.AddCommand(
		newAppsCmd(),
		newArtifactsCmd(),
		newBuildsCmd(),
		newPromoteCmd(),
		newRollbackCmd(),
		newStatusCmd(),
		newHistoryCmd(),
		newDiffCmd(),
		newEnvCmd(),
	)
}

// defaultServerAddr reads APP_REGISTRY_ADDRESS, matching the ADDRESS
// convention other CLIs in this repo use for their target service.
func defaultServerAddr() string {
	if addr := os.Getenv("APP_REGISTRY_ADDRESS"); addr != "" {
		return addr
	}
	return "localhost:50051"
}
