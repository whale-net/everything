package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/whale-net/everything/tools/app_registry/apierrors"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	serverAddr string
	format     string
)

var rootCmd = NewRootCmd()

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

// exitVersionSkew is the process exit code Execute uses when an RPC fails
// with codes.Unimplemented -- the deployed app-registry-api does not have a
// method this CLI build calls (issue #570, the general case #569 was one
// instance of). Unlike a plain outage (Unavailable, DeadlineExceeded,
// Unauthenticated -- all still exit 1), Unimplemented does not clear on
// retry: it means CI's CLI and the deployed server have drifted, and only
// redeploying the server (or turning off APP_REGISTRY_CICD_OPT_IN) fixes it.
// Composite actions use this to escalate their annotation from `::warning::`
// (transient, best-effort-and-move-on) to `::error::` (deployment defect,
// loud at every adoption stage) -- see
// tools/app_registry/ARCHITECTURE.md#availability-and-bootstrap.
//
// Keep this constant's value in sync with every GitHub Actions call site
// that checks for it literally, since a shell script can't import this
// package -- see the "$RECORD_EXIT -eq 4" (or equivalent) branches across
// .github/actions/app-registry-*/action.yml and release.yml's inline chart
// loops.
const exitVersionSkew = 4

// Execute runs the CLI. Called from main.go.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCodeFor(err))
	}
}

// exitCodeFor classifies err into a process exit code. Every failure other
// than the two carved-out cases below keeps the CLI's long-standing exit
// code 1 -- this is not a general error-taxonomy exit-code scheme, just the
// specific, structured signals a caller can rely on.
func exitCodeFor(err error) int {
	if isOwnerNotReconciled(err) {
		return exitOwnerNotReconciled
	}
	if isVersionSkew(err) {
		return exitVersionSkew
	}
	return 1
}

// isVersionSkew reports whether err is a gRPC status with
// codes.Unimplemented -- the server doesn't have the method at all, as
// opposed to rejecting the call for a reason of its own (those come back as
// InvalidArgument, FailedPrecondition, etc. and fall through to exit 1 like
// always). A non-gRPC error naturally reports false.
func isVersionSkew(err error) bool {
	st, ok := status.FromError(err)
	return ok && st.Code() == codes.Unimplemented
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

// NewRootCmd builds a complete, independent command tree. Package-level
// state (the flag variables above, and each subcommand constructor's own
// closed-over flag variables) is bound freshly per call, so two trees never
// share flag values.
//
// Exported, and a constructor rather than the usual package-level singleton
// plus init(), because callers that run more than one command in one process
// need that independence: cobra writes parsed flags into the variables a
// command's constructor closed over, so a reused tree carries the previous
// invocation's values into the next one. //tools/app_registry/citest drives
// exactly the command lines .github/workflows/release.yml runs, in sequence,
// against one in-process server -- see that package's doc comment.
func NewRootCmd() *cobra.Command {
	c := &cobra.Command{
		Use:          "app-registry",
		Short:        "Thin gRPC client for the App Registry",
		Long:         "app-registry is a thin gRPC client for the App Registry. It validates argument shape, calls one RPC, and formats the response — no business logic lives here.",
		SilenceUsage: true,
	}
	c.SilenceErrors = true
	c.PersistentFlags().StringVar(&serverAddr, "address", defaultServerAddr(), "app-registry-api address (host:port)")
	c.PersistentFlags().StringVar(&format, "format", "json", "Output format: json or table")

	c.AddCommand(
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
	return c
}

// ExitCodeFor exposes exitCodeFor's classification so a caller running the
// CLI in-process can assert on the same exit code a CI shell script would
// branch on ($? == 3 / 4) rather than re-deriving it.
func ExitCodeFor(err error) int { return exitCodeFor(err) }

// defaultServerAddr reads APP_REGISTRY_ADDRESS, matching the ADDRESS
// convention other CLIs in this repo use for their target service.
func defaultServerAddr() string {
	if addr := os.Getenv("APP_REGISTRY_ADDRESS"); addr != "" {
		return addr
	}
	return "localhost:50051"
}
