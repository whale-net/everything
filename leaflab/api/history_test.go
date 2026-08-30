package main

// Validation-only tests for GetSensorReadingHistory (FR8, FR9): every case
// here returns before the handler ever calls into the repository, so it's
// exercised with a Repository wrapping a nil pool instead of a real
// database -- a nil dereference would be the failure signal if any of
// these paths ever started touching the DB before its guard fires. The
// cap/invalid-count/NotFound/empty-range behaviour that *does* need a real
// database lives in history_integration_test.go (`//go:build integration`).

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

func newValidationOnlyServer() *LeafLabAPIServer {
	return NewLeafLabAPIServer(NewRepository(nil), nil, slog.Default())
}

func TestGetSensorReadingHistory_MissingFromOrTo_InvalidArgument(t *testing.T) {
	srv := newValidationOnlyServer()
	now := timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	cases := []struct {
		name string
		from *timestamppb.Timestamp
		to   *timestamppb.Timestamp
	}{
		{name: "both nil", from: nil, to: nil},
		{name: "from nil", from: nil, to: now},
		{name: "to nil", from: now, to: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.GetSensorReadingHistory(context.Background(), &pb.GetSensorReadingHistoryRequest{
				SensorId: 1,
				From:     tc.from,
				To:       tc.to,
			})
			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestGetSensorReadingHistory_ToNotAfterFrom_InvalidArgument(t *testing.T) {
	srv := newValidationOnlyServer()
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		from time.Time
		to   time.Time
	}{
		{name: "to equal from", from: base, to: base},
		{name: "to before from", from: base, to: base.Add(-time.Minute)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.GetSensorReadingHistory(context.Background(), &pb.GetSensorReadingHistoryRequest{
				SensorId: 1,
				From:     timestamppb.New(tc.from),
				To:       timestamppb.New(tc.to),
			})
			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestGetSensorReadingHistory_RangeOver30Days_InvalidArgument(t *testing.T) {
	srv := newValidationOnlyServer()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(31 * 24 * time.Hour)

	_, err := srv.GetSensorReadingHistory(context.Background(), &pb.GetSensorReadingHistoryRequest{
		SensorId: 1,
		From:     timestamppb.New(from),
		To:       timestamppb.New(to),
	})

	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "30", "error should name the day limit so it's clear which guard fired")
}
