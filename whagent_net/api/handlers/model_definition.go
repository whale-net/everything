package handlers

import (
	"context"
	"encoding/json"

	pb "github.com/whale-net/everything/whagent_net/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListModelDefinitions returns every model_definition row ordered by model
// then id. Any authenticated caller may call it.
func (s *SessionServer) ListModelDefinitions(ctx context.Context, _ *pb.ListModelDefinitionsRequest) (*pb.ListModelDefinitionsResponse, error) {
	defs, err := s.store.ModelDefinitions().List(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list model definitions: %v", err)
	}
	resp := &pb.ListModelDefinitionsResponse{}
	for _, d := range defs {
		prov, err := json.Marshal(d.Provider)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encode provider: %v", err)
		}
		resp.ModelDefinitions = append(resp.ModelDefinitions, &pb.ModelDefinition{
			Id: d.ID.String(), Name: d.Name, Model: d.Model, Provider: string(prov),
		})
	}
	return resp, nil
}
