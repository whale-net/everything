package main

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/grpcclient"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// LeafLabClient wraps the leaflab-api gRPC client. Every call made through
// this client must carry the signed-in user's own access token via
// grpcauth.WithUserToken on the request context (NFR2) — see
// libs/go/htmxauth's Authenticator.WithAccessToken, which every protected
// route in main.go's setupRoutes is wrapped in.
type LeafLabClient struct {
	conn *grpcclient.Client
	api  leaflabapipb.LeafLabAPIClient
}

// NewLeafLabClient dials leaflab-api once at startup. extraOpts carries the
// grpcauth.NewUserTokenDialOption(...) dial option that forwards each
// request's user token.
func NewLeafLabClient(ctx context.Context, addr string, extraOpts ...grpc.DialOption) (*LeafLabClient, error) {
	conn, err := grpcclient.NewClient(ctx, addr, extraOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to leaflab-api: %w", err)
	}

	return &LeafLabClient{
		conn: conn,
		api:  leaflabapipb.NewLeafLabAPIClient(conn.GetConnection()),
	}, nil
}

// Close closes the gRPC connection.
func (c *LeafLabClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// ListBoardsWithState lists every known board with its reporting state
// (FR4/FR5 of #1502's boards-list screen, handlers_boards.go).
func (c *LeafLabClient) ListBoardsWithState(ctx context.Context) (*leaflabapipb.ListBoardsWithStateResponse, error) {
	resp, err := c.api.ListBoardsWithState(ctx, &leaflabapipb.ListBoardsWithStateRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list boards: %w", err)
	}
	return resp, nil
}

// GetBoardDetail fetches one board's identity plus every sensor recorded
// for it, each with its own reporting state and (when present) most
// recent reading (FR6/FR7 of #1503's board-detail screen,
// handlers_boards.go). The error is returned wrapped with %w so a caller's
// status.FromError(err) still sees the underlying gRPC status (e.g.
// codes.NotFound for an unknown board_id, codes.Unauthenticated for a
// rejected token) -- status.FromError unwraps via errors.As.
func (c *LeafLabClient) GetBoardDetail(ctx context.Context, boardID int64) (*leaflabapipb.GetBoardDetailResponse, error) {
	resp, err := c.api.GetBoardDetail(ctx, &leaflabapipb.GetBoardDetailRequest{BoardId: boardID})
	if err != nil {
		return nil, fmt.Errorf("failed to get board detail for board %d: %w", boardID, err)
	}
	return resp, nil
}

// GetSensorReadingHistory fetches one sensor's raw readings over an
// absolute [from, to) range (#1504: FR8, FR9, FR10 of the sensor
// reading-history chart, handlers_sensors.go). The error is returned
// wrapped with %w so a caller's status.FromError(err) still sees the
// underlying gRPC status (e.g. codes.NotFound for an unknown sensor_id,
// codes.InvalidArgument for a range over 30 days, codes.Unauthenticated
// for a rejected token).
func (c *LeafLabClient) GetSensorReadingHistory(ctx context.Context, sensorID int64, from, to time.Time) (*leaflabapipb.GetSensorReadingHistoryResponse, error) {
	resp, err := c.api.GetSensorReadingHistory(ctx, &leaflabapipb.GetSensorReadingHistoryRequest{
		SensorId: sensorID,
		From:     timestamppb.New(from),
		To:       timestamppb.New(to),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get sensor reading history for sensor %d: %w", sensorID, err)
	}
	return resp, nil
}

// ClaimBoard claims an unowned board for the calling user (FR1, FR2). The
// "already owned" case comes back as codes.FailedPrecondition on the
// wrapped error -- status.FromError(err) still sees it -- there is no
// success flag to check.
func (c *LeafLabClient) ClaimBoard(ctx context.Context, boardID int64) (*leaflabapipb.ClaimBoardResponse, error) {
	resp, err := c.api.ClaimBoard(ctx, &leaflabapipb.ClaimBoardRequest{BoardId: boardID})
	if err != nil {
		return nil, fmt.Errorf("failed to claim board %d: %w", boardID, err)
	}
	return resp, nil
}

// RenameBoard renames a board the calling user owns (FR3).
func (c *LeafLabClient) RenameBoard(ctx context.Context, boardID int64, name string) (*leaflabapipb.RenameBoardResponse, error) {
	resp, err := c.api.RenameBoard(ctx, &leaflabapipb.RenameBoardRequest{BoardId: boardID, Name: name})
	if err != nil {
		return nil, fmt.Errorf("failed to rename board %d: %w", boardID, err)
	}
	return resp, nil
}

// RenameSensor renames a sensor on a board the calling user owns (FR4).
func (c *LeafLabClient) RenameSensor(ctx context.Context, sensorID int64, name string) (*leaflabapipb.RenameSensorResponse, error) {
	resp, err := c.api.RenameSensor(ctx, &leaflabapipb.RenameSensorRequest{SensorId: sensorID, Name: name})
	if err != nil {
		return nil, fmt.Errorf("failed to rename sensor %d: %w", sensorID, err)
	}
	return resp, nil
}

// ListOwnedBoards lists every currently-owned board and its current owner
// (FR11, admin ownership screen).
func (c *LeafLabClient) ListOwnedBoards(ctx context.Context) (*leaflabapipb.ListOwnedBoardsResponse, error) {
	resp, err := c.api.ListOwnedBoards(ctx, &leaflabapipb.ListOwnedBoardsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list owned boards: %w", err)
	}
	return resp, nil
}

// ReassignBoardOwner closes the current ownership record on a board and
// opens a new one for the given user (FR12, admin ownership screen).
func (c *LeafLabClient) ReassignBoardOwner(ctx context.Context, boardID, newOwnerLeafLabUserID int64) (*leaflabapipb.ReassignBoardOwnerResponse, error) {
	resp, err := c.api.ReassignBoardOwner(ctx, &leaflabapipb.ReassignBoardOwnerRequest{
		BoardId:               boardID,
		NewOwnerLeaflabUserId: newOwnerLeafLabUserID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to reassign owner for board %d: %w", boardID, err)
	}
	return resp, nil
}

// ClearBoardOwner closes the current ownership record on a board and opens
// none (FR13, admin ownership screen).
func (c *LeafLabClient) ClearBoardOwner(ctx context.Context, boardID int64) (*leaflabapipb.ClearBoardOwnerResponse, error) {
	resp, err := c.api.ClearBoardOwner(ctx, &leaflabapipb.ClearBoardOwnerRequest{BoardId: boardID})
	if err != nil {
		return nil, fmt.Errorf("failed to clear owner for board %d: %w", boardID, err)
	}
	return resp, nil
}

// ListUsers lists every leaflab user, the source list the admin reassign
// picker selects from (FR11, FR12).
func (c *LeafLabClient) ListUsers(ctx context.Context) (*leaflabapipb.ListUsersResponse, error) {
	resp, err := c.api.ListUsers(ctx, &leaflabapipb.ListUsersRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list users: %w", err)
	}
	return resp, nil
}

// -- M3: region management (#2317) -------------------------------------------

// GetRegionTree fetches the region tree view (FR6): the whole forest of
// top-level regions (alphabetically ordered) when rootRegionID is 0, or the
// subtree rooted at rootRegionID alone when non-zero (drill-down). Children
// arrive alphabetically ordered; both sensor counts (here only / here or
// below) are the API's CURRENT-placement numbers, rendered verbatim -- the
// UI recomputes neither. The error is returned wrapped with %w so a caller's
// status.FromError(err) still sees the underlying gRPC status (e.g.
// codes.NotFound for an unknown root_region_id, codes.Unauthenticated for a
// rejected token).
func (c *LeafLabClient) GetRegionTree(ctx context.Context, rootRegionID int64) (*leaflabapipb.GetRegionTreeResponse, error) {
	resp, err := c.api.GetRegionTree(ctx, &leaflabapipb.GetRegionTreeRequest{RootRegionId: rootRegionID})
	if err != nil {
		return nil, fmt.Errorf("failed to get region tree (root %d): %w", rootRegionID, err)
	}
	return resp, nil
}

// CreateRegion creates a region owned by the calling user (FR1), nested
// under parentRegionID when non-zero or top-level when 0. The created
// region's ID comes back for drill-down navigation; failures (empty name,
// unknown parent) come back as gRPC statuses on the wrapped error --
// status.FromError(err) still sees them.
func (c *LeafLabClient) CreateRegion(ctx context.Context, name string, parentRegionID int64) (*leaflabapipb.CreateRegionResponse, error) {
	resp, err := c.api.CreateRegion(ctx, &leaflabapipb.CreateRegionRequest{
		Name:           name,
		ParentRegionId: parentRegionID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create region: %w", err)
	}
	return resp, nil
}

// RenameRegion renames a region the calling user owns (FR2, forward-looking
// only). Authorization is enforced server-side (NFR2); a non-owner's call
// comes back as codes.PermissionDenied on the wrapped error.
func (c *LeafLabClient) RenameRegion(ctx context.Context, regionID int64, name string) (*leaflabapipb.RenameRegionResponse, error) {
	resp, err := c.api.RenameRegion(ctx, &leaflabapipb.RenameRegionRequest{
		RegionId: regionID,
		Name:     name,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to rename region %d: %w", regionID, err)
	}
	return resp, nil
}

// ReparentRegion re-parents a region (FR3), including to top-level when
// parentRegionID is 0. A re-parent into the region itself or one of its
// descendants is rejected server-side as codes.FailedPrecondition (FR5) on
// the wrapped error -- the UI surfaces that message rather than pre-filtering
// picker options, so the API stays the single cycle-enforcement point.
func (c *LeafLabClient) ReparentRegion(ctx context.Context, regionID, parentRegionID int64) (*leaflabapipb.ReparentRegionResponse, error) {
	resp, err := c.api.ReparentRegion(ctx, &leaflabapipb.ReparentRegionRequest{
		RegionId:       regionID,
		ParentRegionId: parentRegionID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to re-parent region %d: %w", regionID, err)
	}
	return resp, nil
}

// PlaceSensor places a sensor in a region (FR7), whether the sensor had a
// prior placement (assign) or not (move): PlaceSensor is the only placement
// write, and the API rejects unassigning outright (region_id is required,
// > 0). The write is an ordinary Postgres write (LB2): it never waits on,
// or is gated by, a device round trip, and is visible to the very next
// GetBoardDetail. Failures (unknown sensor/region, non-owner, region_id <=
// 0) come back as gRPC statuses on the wrapped error -- the direct
// errors.As GRPCStatus() extraction placementWriteErrorMessage uses (see
// handlers_boards.go) still sees the original, unrewritten status.
func (c *LeafLabClient) PlaceSensor(ctx context.Context, sensorID, regionID int64) (*leaflabapipb.PlaceSensorResponse, error) {
	resp, err := c.api.PlaceSensor(ctx, &leaflabapipb.PlaceSensorRequest{
		SensorId: sensorID,
		RegionId: regionID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to place sensor %d: %w", sensorID, err)
	}
	return resp, nil
}

// SetBoardRegion records, changes, or clears the region a board is
// physically located in (FR10). regionID nil clears the recorded region;
// there is no "region_id = 0" sentinel on the wire. Bookkeeping only
// server-side: the write never touches sensor placement or reading
// attribution (FR10) -- this UI's FR11 nudge is likewise display-only.
// The response carries the board's post-write recorded region plus every
// sensor with its current placement (the FR11 nudge input); no-op refusals
// (recording the already-recorded region, clearing an unrecorded board)
// come back as codes.FailedPrecondition on the wrapped error.
func (c *LeafLabClient) SetBoardRegion(ctx context.Context, boardID int64, regionID *int64) (*leaflabapipb.SetBoardRegionResponse, error) {
	req := &leaflabapipb.SetBoardRegionRequest{BoardId: boardID}
	if regionID != nil {
		req.RegionId = regionID
	}
	resp, err := c.api.SetBoardRegion(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to set recorded region for board %d: %w", boardID, err)
	}
	return resp, nil
}
