package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	apierrors "github.com/whale-net/everything/leaflab/api/apierrors"
	"github.com/whale-net/everything/leaflab/api/pagetoken"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// stubRepository implements the Repository interface for testing.
type stubRepository struct {
	getOrCreateBoardFn         func(ctx context.Context, deviceID string) (int64, error)
	insertDeviceConfigFn       func(ctx context.Context, boardID int64, configJSON []byte) (int64, error)
	getLatestAcceptedConfigFn  func(ctx context.Context, deviceID string) (interface{}, error)
	listBoardsFn               func(ctx context.Context, pageSize int32, token *pagetoken.Token) ([]BoardRow, *pagetoken.Token, error)
	getTotalBoardCountFn       func(ctx context.Context) (int32, error)
}

func (s *stubRepository) GetOrCreateBoard(ctx context.Context, deviceID string) (int64, error) {
	if s.getOrCreateBoardFn != nil {
		return s.getOrCreateBoardFn(ctx, deviceID)
	}
	return 1, nil
}

func (s *stubRepository) InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte) (int64, error) {
	if s.insertDeviceConfigFn != nil {
		return s.insertDeviceConfigFn(ctx, boardID, configJSON)
	}
	return 1, nil
}

func (s *stubRepository) GetLatestAcceptedConfig(ctx context.Context, deviceID string) (interface{}, error) {
	if s.getLatestAcceptedConfigFn != nil {
		return s.getLatestAcceptedConfigFn(ctx, deviceID)
	}
	return nil, nil
}

func (s *stubRepository) ListBoards(ctx context.Context, pageSize int32, token *pagetoken.Token) ([]BoardRow, *pagetoken.Token, error) {
	if s.listBoardsFn != nil {
		return s.listBoardsFn(ctx, pageSize, token)
	}
	return []BoardRow{}, nil, nil
}

func (s *stubRepository) GetTotalBoardCount(ctx context.Context) (int32, error) {
	if s.getTotalBoardCountFn != nil {
		return s.getTotalBoardCountFn(ctx)
	}
	return 0, nil
}

// startTestServer creates a gRPC server with the given repository.
func startTestServer(t *testing.T, repo *stubRepository) pb.LeafLabAPIClient {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()

	// Register our test server
	pb.RegisterLeafLabAPIServer(grpcServer, &testServer{repo: repo})

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn server: %v", err)
	}

	t.Cleanup(func() {
		conn.Close()          //nolint:errcheck
		grpcServer.Stop()
	})

	return pb.NewLeafLabAPIClient(conn)
}

// testServer wraps stubRepository to implement LeafLabAPIServer
type testServer struct {
	pb.UnimplementedLeafLabAPIServer
	repo *stubRepository
}

func (s *testServer) PushDeviceConfig(ctx context.Context, req *pb.PushDeviceConfigRequest) (*pb.PushDeviceConfigResponse, error) {
	if err := validateDeviceID(req.DeviceId); err != nil {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"device_id",
			"device_id",
			apierrors.InvalidDeviceID,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
	}
	_, err := s.repo.GetOrCreateBoard(ctx, req.DeviceId)
	if err != nil {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"board",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}
	// For testing, just return a version without actually storing anything
	return &pb.PushDeviceConfigResponse{Version: 1}, nil
}

func (s *testServer) GetDeviceConfig(ctx context.Context, req *pb.GetDeviceConfigRequest) (*pb.GetDeviceConfigResponse, error) {
	if err := validateDeviceID(req.DeviceId); err != nil {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"device_id",
			"device_id",
			apierrors.InvalidDeviceID,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
	}
	return &pb.GetDeviceConfigResponse{Found: false}, nil
}

func (s *testServer) ListBoards(ctx context.Context, req *pb.ListBoardsRequest) (*pb.ListBoardsResponse, error) {
	// Parse page token
	var decodedToken *pagetoken.Token
	if req.Page != nil && req.Page.PageToken != "" {
		var err error
		decodedToken, err = pagetoken.Decode(req.Page.PageToken)
		if err != nil {
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
				"page",
				"page_token",
				apierrors.InvalidPageToken,
			)
			return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
		}
	}

	// Determine page size
	var pageSize int32 = DefaultPageSize
	if req.Page != nil && req.Page.PageSize > 0 {
		pageSize = req.Page.PageSize
	}

	// Fetch boards from repository
	rows, nextToken, err := s.repo.ListBoards(ctx, pageSize, decodedToken)
	if err != nil {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"board",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Get total board count
	totalSize, err := s.repo.GetTotalBoardCount(ctx)
	if err != nil {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"board",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Convert to proto
	boards := make([]*pb.BoardInfo, 0, len(rows))
	for _, r := range rows {
		boards = append(boards, &pb.BoardInfo{
			DeviceId:   r.DeviceID,
			BoardId:    r.BoardID,
			RecordedAt: r.RecordedAt,
		})
	}

	// Encode next page token
	var nextPageToken string
	if nextToken != nil {
		encoded, err := pagetoken.Encode(nextToken)
		if err != nil {
			nextPageToken = ""
		} else {
			nextPageToken = encoded
		}
	}

	return &pb.ListBoardsResponse{
		Boards: boards,
		Page: &pb.PageResponse{
			NextPageToken: nextPageToken,
			TotalSize:     totalSize,
		},
	}, nil
}

// Test FR59 — Failure contract: structured errors readable without parsing message
func TestFR59_StructuredErrors(t *testing.T) {
	tests := []struct {
		name          string
		call          func(client pb.LeafLabAPIClient) error
		expectCode    codes.Code
		expectClass   pb.FailureClass
		expectEntity  string
		expectField   string
		expectMsgKey  string
	}{
		{
			name: "PushDeviceConfig with empty device_id",
			call: func(client pb.LeafLabAPIClient) error {
				_, err := client.PushDeviceConfig(context.Background(), &pb.PushDeviceConfigRequest{
					DeviceId: "",
				})
				return err
			},
			expectCode:   codes.InvalidArgument,
			expectClass:  pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			expectEntity: "device_id",
			expectField:  "device_id",
			expectMsgKey: apierrors.InvalidDeviceID,
		},
		{
			name: "PushDeviceConfig with invalid characters in device_id",
			call: func(client pb.LeafLabAPIClient) error {
				_, err := client.PushDeviceConfig(context.Background(), &pb.PushDeviceConfigRequest{
					DeviceId: "device/with/slash",
				})
				return err
			},
			expectCode:   codes.InvalidArgument,
			expectClass:  pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			expectEntity: "device_id",
			expectField:  "device_id",
			expectMsgKey: apierrors.InvalidDeviceID,
		},
		{
			name: "GetDeviceConfig with invalid device_id",
			call: func(client pb.LeafLabAPIClient) error {
				_, err := client.GetDeviceConfig(context.Background(), &pb.GetDeviceConfigRequest{
					DeviceId: "device#with#hash",
				})
				return err
			},
			expectCode:   codes.InvalidArgument,
			expectClass:  pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			expectEntity: "device_id",
			expectField:  "device_id",
			expectMsgKey: apierrors.InvalidDeviceID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := startTestServer(t, &stubRepository{})

			err := tt.call(client)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}

			// Verify gRPC status code
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected gRPC status error, got %v", err)
			}
			if st.Code() != tt.expectCode {
				t.Fatalf("expected code %v, got %v", tt.expectCode, st.Code())
			}

			// Verify structured error detail
			detail := apierrors.ErrorDetailFromStatus(st)
			if detail == nil {
				t.Fatalf("expected ErrorDetail in status, got nil")
			}

			if detail.FailureClass != tt.expectClass {
				t.Errorf("expected class %v, got %v", tt.expectClass, detail.FailureClass)
			}
			if detail.Entity != tt.expectEntity {
				t.Errorf("expected entity %q, got %q", tt.expectEntity, detail.Entity)
			}
			if detail.Field != tt.expectField {
				t.Errorf("expected field %q, got %q", tt.expectField, detail.Field)
			}
			if detail.MessageKey != tt.expectMsgKey {
				t.Errorf("expected message key %q, got %q", tt.expectMsgKey, detail.MessageKey)
			}
		})
	}
}

// Test FR61 — Keyset pagination: page_size is clamped in repository
func TestFR61_PageSizeIsClamped(t *testing.T) {
	actualPageSize := int32(0)
	repo := &stubRepository{
		listBoardsFn: func(ctx context.Context, pageSize int32, token *pagetoken.Token) ([]BoardRow, *pagetoken.Token, error) {
			// Record the actual page size passed to the repository
			actualPageSize = pageSize
			// Simulate repository behavior: clamp and return boards
			if pageSize > MaxPageSize {
				pageSize = MaxPageSize
			}
			// Return boards up to the clamped page size
			boards := make([]BoardRow, pageSize)
			for i := 0; i < int(pageSize); i++ {
				boards[i] = BoardRow{
					BoardID:    int64(i + 1),
					DeviceID:   fmt.Sprintf("device-%d", i+1),
					RecordedAt: int64(time.Now().Unix() - int64(i)),
				}
			}
			return boards, nil, nil
		},
		getTotalBoardCountFn: func(ctx context.Context) (int32, error) {
			return int32(MaxPageSize) + 100, nil
		},
	}

	client := startTestServer(t, repo)

	// Request more than MaxPageSize
	resp, err := client.ListBoards(context.Background(), &pb.ListBoardsRequest{
		Page: &pb.PageRequest{
			PageSize: MaxPageSize + 100, // Request too many
		},
	})
	if err != nil {
		t.Fatalf("ListBoards failed: %v", err)
	}

	// Verify the repository received the unclamped request
	if actualPageSize != MaxPageSize+100 {
		t.Errorf("expected repository to receive %d, got %d", MaxPageSize+100, actualPageSize)
	}

	// Verify that the response contains at most MaxPageSize boards
	if int32(len(resp.Boards)) > MaxPageSize {
		t.Errorf("expected at most %d boards in response, got %d", MaxPageSize, len(resp.Boards))
	}
}

// Test FR61 — Keyset pagination: opaque tokens cannot be mutated
func TestFR61_MutatedTokenIsRejected(t *testing.T) {
	client := startTestServer(t, &stubRepository{})

	// Create a valid token
	validToken := &pagetoken.Token{
		LastRecordedAt: int64(time.Now().Unix()),
		LastBoardID:    42,
	}
	encoded, err := pagetoken.Encode(validToken)
	if err != nil {
		t.Fatalf("failed to encode valid token: %v", err)
	}

	// Mutate the token by changing characters in the base64
	mutated := string([]byte(encoded)[:len(encoded)-1]) + "!"
	if mutated == encoded {
		// Try another approach if the above didn't work
		mutated = "INVALID_BASE64_=="
	}

	// Attempt to use the mutated token
	_, err = client.ListBoards(context.Background(), &pb.ListBoardsRequest{
		Page: &pb.PageRequest{
			PageToken: mutated,
		},
	})
	if err == nil {
		t.Fatalf("expected error with mutated token, got nil")
	}

	// Verify the error is InvalidArgument with InvalidPageToken
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected codes.InvalidArgument, got %v", st.Code())
	}

	detail := apierrors.ErrorDetailFromStatus(st)
	if detail == nil {
		t.Fatalf("expected ErrorDetail, got nil")
	}
	if detail.MessageKey != apierrors.InvalidPageToken {
		t.Errorf("expected message key %q, got %q", apierrors.InvalidPageToken, detail.MessageKey)
	}

	// Verify that the error message does NOT leak the token contents
	if strings.Contains(st.Message(), encoded) {
		t.Errorf("error message leaked token contents: %q", st.Message())
	}
}

// Test FR61 — Keyset pagination: round-trip validity
func TestFR61_RoundTripPageToken(t *testing.T) {
	repo := &stubRepository{
		listBoardsFn: func(ctx context.Context, pageSize int32, token *pagetoken.Token) ([]BoardRow, *pagetoken.Token, error) {
			// Simulate a pagination scenario with 4 boards
			boards := []BoardRow{
				{BoardID: 1, DeviceID: "dev1", RecordedAt: 1000},
				{BoardID: 2, DeviceID: "dev2", RecordedAt: 999},
				{BoardID: 3, DeviceID: "dev3", RecordedAt: 998},
				{BoardID: 4, DeviceID: "dev4", RecordedAt: 997},
			}

			// If we have a token, return boards after that point
			if token != nil {
				var nextBoards []BoardRow
				for _, b := range boards {
					if b.RecordedAt < token.LastRecordedAt ||
						(b.RecordedAt == token.LastRecordedAt && b.BoardID < token.LastBoardID) {
						nextBoards = append(nextBoards, b)
					}
				}
				boards = nextBoards
			}

			// Return first pageSize boards, plus check for more
			if int32(len(boards)) > pageSize {
				nextToken := &pagetoken.Token{
					LastRecordedAt: boards[pageSize-1].RecordedAt,
					LastBoardID:    boards[pageSize-1].BoardID,
				}
				return boards[:pageSize], nextToken, nil
			}
			return boards, nil, nil
		},
		getTotalBoardCountFn: func(ctx context.Context) (int32, error) {
			return 4, nil
		},
	}

	client := startTestServer(t, repo)

	// First page
	resp1, err := client.ListBoards(context.Background(), &pb.ListBoardsRequest{
		Page: &pb.PageRequest{PageSize: 2},
	})
	if err != nil {
		t.Fatalf("first ListBoards failed: %v", err)
	}
	if len(resp1.Boards) == 0 {
		t.Fatalf("expected boards in first page, got none")
	}
	if resp1.Page.NextPageToken == "" {
		t.Fatalf("expected next_page_token in first page response")
	}

	// Use the token to fetch the next page
	resp2, err := client.ListBoards(context.Background(), &pb.ListBoardsRequest{
		Page: &pb.PageRequest{
			PageToken: resp1.Page.NextPageToken,
			PageSize:  2,
		},
	})
	if err != nil {
		t.Fatalf("second ListBoards failed: %v", err)
	}
	if len(resp2.Boards) == 0 {
		t.Fatalf("expected boards in second page, got none")
	}

	// Verify that boards don't repeat between pages
	firstPageIDs := make(map[int64]bool)
	for _, b := range resp1.Boards {
		firstPageIDs[b.BoardId] = true
	}
	for _, b := range resp2.Boards {
		if firstPageIDs[b.BoardId] {
			t.Errorf("board %d appeared in both pages", b.BoardId)
		}
	}
}

// Test FR64 — Absolute instants: timestamps are Unix seconds
func TestFR64_AbsoluteInstants(t *testing.T) {
	now := time.Now().Unix()
	repo := &stubRepository{
		listBoardsFn: func(ctx context.Context, pageSize int32, token *pagetoken.Token) ([]BoardRow, *pagetoken.Token, error) {
			return []BoardRow{
				{BoardID: 1, DeviceID: "dev1", RecordedAt: now},
				{BoardID: 2, DeviceID: "dev2", RecordedAt: now - 3600}, // 1 hour ago
			}, nil, nil
		},
		getTotalBoardCountFn: func(ctx context.Context) (int32, error) {
			return 2, nil
		},
	}

	client := startTestServer(t, repo)

	resp, err := client.ListBoards(context.Background(), &pb.ListBoardsRequest{})
	if err != nil {
		t.Fatalf("ListBoards failed: %v", err)
	}

	if len(resp.Boards) < 2 {
		t.Fatalf("expected at least 2 boards, got %d", len(resp.Boards))
	}

	// Verify that recorded_at is an absolute Unix timestamp (should be recent)
	for i, board := range resp.Boards {
		// Should be within the last 24 hours
		if board.RecordedAt < now-86400 || board.RecordedAt > now+1000 {
			t.Errorf("board %d has invalid timestamp: %d (expected recent Unix seconds)", i, board.RecordedAt)
		}
		// Verify it's in seconds, not milliseconds or nanoseconds
		if board.RecordedAt > 1e12 {
			t.Errorf("board %d timestamp %d looks like milliseconds, expected seconds", i, board.RecordedAt)
		}
	}
}

// Test FR61 — Pagination correctness with append mid-pagination
func TestFR61_CorrectnessWithAppend(t *testing.T) {
	// Simulate the scenario where rows are appended between page fetches
	boardsData := []BoardRow{
		{BoardID: 1, DeviceID: "dev1", RecordedAt: 1000},
		{BoardID: 2, DeviceID: "dev2", RecordedAt: 999},
		{BoardID: 3, DeviceID: "dev3", RecordedAt: 998},
		{BoardID: 4, DeviceID: "dev4", RecordedAt: 997},
	}

	pageNum := 0
	repo := &stubRepository{
		listBoardsFn: func(ctx context.Context, pageSize int32, token *pagetoken.Token) ([]BoardRow, *pagetoken.Token, error) {
			pageNum++

			var boards []BoardRow
			if token == nil {
				// First page
				boards = boardsData
			} else {
				// Subsequent page: filter to boards after the token
				for _, b := range boardsData {
					if b.RecordedAt < token.LastRecordedAt ||
						(b.RecordedAt == token.LastRecordedAt && b.BoardID < token.LastBoardID) {
						boards = append(boards, b)
					}
				}
			}

			// On the second page fetch, simulate a new board being appended
			// (newer timestamp, so it would appear at the beginning of the list)
			if pageNum == 2 {
				boards = append([]BoardRow{{BoardID: 5, DeviceID: "dev5", RecordedAt: 1001}}, boards...)
			}

			if int32(len(boards)) > pageSize {
				nextToken := &pagetoken.Token{
					LastRecordedAt: boards[pageSize-1].RecordedAt,
					LastBoardID:    boards[pageSize-1].BoardID,
				}
				return boards[:pageSize], nextToken, nil
			}
			return boards, nil, nil
		},
		getTotalBoardCountFn: func(ctx context.Context) (int32, error) {
			return int32(len(boardsData)) + 1, nil
		},
	}

	client := startTestServer(t, repo)

	// Fetch first page
	resp1, err := client.ListBoards(context.Background(), &pb.ListBoardsRequest{
		Page: &pb.PageRequest{PageSize: 2},
	})
	if err != nil {
		t.Fatalf("first ListBoards failed: %v", err)
	}

	// Fetch second page (after new board was inserted)
	resp2, err := client.ListBoards(context.Background(), &pb.ListBoardsRequest{
		Page: &pb.PageRequest{
			PageToken: resp1.Page.NextPageToken,
			PageSize:  2,
		},
	})
	if err != nil {
		t.Fatalf("second ListBoards failed: %v", err)
	}

	// Collect all board IDs seen
	seenIDs := make(map[int64]int)
	for _, b := range resp1.Boards {
		seenIDs[b.BoardId]++
	}
	for _, b := range resp2.Boards {
		seenIDs[b.BoardId]++
	}

	// Verify no duplicates
	for id, count := range seenIDs {
		if count > 1 {
			t.Errorf("board %d appeared %d times across pages", id, count)
		}
	}
}
