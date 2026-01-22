package handlers

import (
	"context"
	"encoding/json"

	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/api/repository"
	pb "github.com/whale-net/everything/manman/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GameHandler struct {
	repo repository.GameRepository
}

func NewGameHandler(repo repository.GameRepository) *GameHandler {
	return &GameHandler{repo: repo}
}

func (h *GameHandler) ListGames(ctx context.Context, req *pb.ListGamesRequest) (*pb.ListGamesResponse, error) {
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}

	offset := 0
	if req.PageToken != "" {
		var err error
		offset, err = decodePageToken(req.PageToken)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid page token: %v", err)
		}
	}

	games, err := h.repo.List(ctx, pageSize+1, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list games: %v", err)
	}

	var nextPageToken string
	if len(games) > pageSize {
		games = games[:pageSize]
		nextPageToken = encodePageToken(offset + pageSize)
	}

	pbGames := make([]*pb.Game, len(games))
	for i, g := range games {
		pbGames[i] = gameToProto(g)
	}

	return &pb.ListGamesResponse{
		Games:         pbGames,
		NextPageToken: nextPageToken,
	}, nil
}

func (h *GameHandler) GetGame(ctx context.Context, req *pb.GetGameRequest) (*pb.GetGameResponse, error) {
	game, err := h.repo.Get(ctx, req.GameId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "game not found: %v", err)
	}

	return &pb.GetGameResponse{
		Game: gameToProto(game),
	}, nil
}

func (h *GameHandler) CreateGame(ctx context.Context, req *pb.CreateGameRequest) (*pb.CreateGameResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	game := &manman.Game{
		Name:       req.Name,
		SteamAppID: stringPtr(req.SteamAppId),
	}

	if req.Metadata != "" {
		metadata, err := parseJSONB(req.Metadata)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid metadata JSON: %v", err)
		}
		game.Metadata = metadata
	}

	game, err := h.repo.Create(ctx, game)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create game: %v", err)
	}

	return &pb.CreateGameResponse{
		Game: gameToProto(game),
	}, nil
}

func (h *GameHandler) UpdateGame(ctx context.Context, req *pb.UpdateGameRequest) (*pb.UpdateGameResponse, error) {
	game, err := h.repo.Get(ctx, req.GameId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "game not found: %v", err)
	}

	if req.Name != "" {
		game.Name = req.Name
	}
	if req.SteamAppId != "" {
		game.SteamAppID = stringPtr(req.SteamAppId)
	}
	if req.Metadata != "" {
		metadata, err := parseJSONB(req.Metadata)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid metadata JSON: %v", err)
		}
		game.Metadata = metadata
	}

	if err := h.repo.Update(ctx, game); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update game: %v", err)
	}

	return &pb.UpdateGameResponse{
		Game: gameToProto(game),
	}, nil
}

func (h *GameHandler) DeleteGame(ctx context.Context, req *pb.DeleteGameRequest) (*pb.DeleteGameResponse, error) {
	if err := h.repo.Delete(ctx, req.GameId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete game: %v", err)
	}

	return &pb.DeleteGameResponse{}, nil
}

func gameToProto(g *manman.Game) *pb.Game {
	pbGame := &pb.Game{
		GameId: g.GameID,
		Name:   g.Name,
	}

	if g.SteamAppID != nil {
		pbGame.SteamAppId = *g.SteamAppID
	}

	if g.Metadata != nil {
		pbGame.Metadata = jsonbToString(g.Metadata)
	}

	return pbGame
}

// Helper functions
func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseJSONB(s string) (manman.JSONB, error) {
	if s == "" {
		return nil, nil
	}
	var result manman.JSONB
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func jsonbToString(j manman.JSONB) string {
	if j == nil {
		return ""
	}
	data, _ := json.Marshal(j)
	return string(data)
}
