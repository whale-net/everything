// Package handlers implements the four App Registry gRPC services. Each
// file is a thin adapter — validation and delegation to repository/ — never
// business logic itself; see ../README.md "Conventions".
package handlers

import (
	"context"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AppServer implements pb.AppRegistryServer. AR-1 wires it up with no
// business logic; every RPC returns codes.Unimplemented. AR-2/AR-3 fill in
// the repository-backed implementations.
type AppServer struct {
	pb.UnimplementedAppRegistryServer
}

// NewAppServer constructs an AppServer. Takes no dependencies yet — AR-2
// wires in repository.AppRepository here.
func NewAppServer() *AppServer {
	return &AppServer{}
}

func (s *AppServer) ReconcileApps(ctx context.Context, req *pb.ReconcileAppsRequest) (*pb.ReconcileAppsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ReconcileApps not implemented")
}

func (s *AppServer) ListApps(ctx context.Context, req *pb.ListAppsRequest) (*pb.ListAppsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListApps not implemented")
}

func (s *AppServer) GetApp(ctx context.Context, req *pb.GetAppRequest) (*pb.GetAppResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetApp not implemented")
}

func (s *AppServer) ListCharts(ctx context.Context, req *pb.ListChartsRequest) (*pb.ListChartsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListCharts not implemented")
}

func (s *AppServer) SetAppStatus(ctx context.Context, req *pb.SetAppStatusRequest) (*pb.SetAppStatusResponse, error) {
	return nil, status.Error(codes.Unimplemented, "SetAppStatus not implemented")
}
