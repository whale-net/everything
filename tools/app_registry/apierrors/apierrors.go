// Package apierrors defines the small set of structured gRPC error-detail
// reason codes the App Registry server attaches to specific failures, so a
// caller (the CLI, and through it, CI) can classify a failure without
// string-matching the human-readable message.
//
// This is deliberately its own tiny package rather than living in
// server/repository or server/handlers: the CLI (cli/cmd) must not depend on
// the server's implementation packages just to read a reason constant, and
// the server must not depend on the CLI. Both sides import this package
// instead — the single source of truth for the token.
package apierrors

// ErrorDomain is the errdetails.ErrorInfo.Domain value the App Registry
// server sets on every reason below (see google.golang.org/genproto's
// errdetails.ErrorInfo doc comment for the field's meaning).
const ErrorDomain = "app-registry.whale-net"

// ReasonOwnerNotReconciled identifies RecordArtifact's owner-resolution
// failure: owner_full_name didn't match any known app/chart row. Set by
// server/handlers/errors.go's mapRepoErr when the error wraps
// repository.ErrOwnerNotReconciled (see server/handlers/artifact.go's
// resolveOwner). Read by cli/cmd/root.go to decide the process exit code
// (exitOwnerNotReconciled) and, in turn, by
// .github/actions/app-registry-record-image/action.yml and
// release.yml's inline chart-recording loop to print an actionable
// annotation instead of a generic "registry error" one.
//
// Most commonly this means the release ran ahead of ReconcileApps, which
// runs on push to main via ci.yml — see issue #547 and
// tools/app_registry/ARCHITECTURE.md's "Rejected alternatives" table for why
// that gap is an accepted tradeoff rather than something release.yml patches
// over itself.
const ReasonOwnerNotReconciled = "APP_REGISTRY_OWNER_NOT_RECONCILED"
