package handlers

import (
	"errors"

	"github.com/whale-net/everything/tools/app_registry/apierrors"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mapRepoErr translates a repository domain error into the matching gRPC
// status, falling back to codes.Internal. See repository.go's error vars.
func mapRepoErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, repository.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, repository.ErrOwnerNotReconciled):
		// Checked before the plain ErrInvalidArgument case below (it wraps
		// ErrInvalidArgument, so that case would also match) -- this is the
		// one InvalidArgument that gets a structured detail attached, so a
		// caller (the CLI, cli/cmd/root.go) can classify it as "owner not
		// registered yet" (issue #547) instead of parsing err.Error(). See
		// apierrors.ReasonOwnerNotReconciled's doc comment for the full
		// chain. WithDetails only errors on a malformed proto, which
		// ErrorInfo never is here -- the error return is deliberately
		// ignored in favor of the un-detailed status as a fallback.
		st := status.New(codes.InvalidArgument, err.Error())
		if withDetails, derr := st.WithDetails(&errdetails.ErrorInfo{
			Reason: apierrors.ReasonOwnerNotReconciled,
			Domain: apierrors.ErrorDomain,
		}); derr == nil {
			st = withDetails
		}
		return st.Err()
	case errors.Is(err, repository.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, repository.ErrFailedPrecondition):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Errorf(codes.Internal, "internal error: %v", err)
	}
}
