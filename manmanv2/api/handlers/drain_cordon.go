package handlers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/whale-net/everything/manmanv2/api/repository"
	manman "github.com/whale-net/everything/manmanv2/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// cordonError is returned by assertHostSchedulable when a host is not
// schedulable (draining or drained). It implements the standard
// GRPCStatus() interface so codes.FailedPrecondition and the host-naming
// message cross the RPC boundary exactly like any other FailedPrecondition
// (#2364, FR3) -- but it stays identifiable in-process via errors.As, so
// SessionRestartConsumer (same package) can treat a cordon rejection as
// terminal without conflating it with the unrelated commit-race
// FailedPrecondition its retry loop exists to absorb (see the comment on
// startSessionRetryAttempts in api.go).
type cordonError struct {
	serverID   int64
	serverName string
	drainState string
}

func (e *cordonError) Error() string {
	return fmt.Sprintf("host %q (server_id=%d) is %s and not accepting new sessions", e.serverName, e.serverID, e.drainState)
}

func (e *cordonError) GRPCStatus() *status.Status {
	return status.New(codes.FailedPrecondition, e.Error())
}

// assertHostSchedulable is the shared cordon guard (#2364, manmanv2 M6,
// FR3/FR4). Every path that would place a session on a host -- new
// deployment creation (DeployGameConfig), session start (StartSession), and
// session restart (RestartDeployment) -- calls this before doing so.
//
// A draining or drained host returns a *cordonError naming the host,
// never a silent no-op. A schedulable host -- the default, and the only
// state that existed before #2360 -- returns nil, so this guard is a true
// no-op for every caller that predates drain state (AC6 regression
// safety).
//
// The server is loaded fresh on every call rather than accepting a
// pre-fetched *manman.Server, so the check always reflects the current
// drain_state and never a copy that raced a concurrent DrainServer/
// UndrainServer.
func assertHostSchedulable(ctx context.Context, serverRepo repository.ServerRepository, serverID int64) error {
	server, err := serverRepo.Get(ctx, serverID)
	if err != nil {
		// Failure to *read* drain state is a genuine failure, not expected
		// control flow -- ERROR per AGENTS.md logging levels.
		slog.Error("failed to load server for drain cordon check", "server_id", serverID, "error", err)
		return status.Errorf(codes.Internal, "failed to load server %d for drain check: %v", serverID, err)
	}
	if server.DrainState != manman.ServerDrainStateSchedulable {
		// A cordon rejection is expected, handled control flow -- INFO, not
		// WARNING/ERROR, per AGENTS.md logging levels.
		slog.Info("blocked placement on non-schedulable host",
			"server_id", server.ServerID, "server_name", server.Name, "drain_state", server.DrainState)
		return &cordonError{serverID: server.ServerID, serverName: server.Name, drainState: server.DrainState}
	}
	return nil
}
