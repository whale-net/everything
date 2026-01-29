package handlers

import (
	"context"

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
		Metadata:   metadataToJSONB(req.Metadata),
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

	// Apply field mask
	if req.UpdateMask == nil || len(req.UpdateMask.Paths) == 0 {
		// Update all provided fields
		if req.Name != "" {
			game.Name = req.Name
		}
		if req.SteamAppId != "" {
			game.SteamAppID = stringPtr(req.SteamAppId)
		}
		if req.Metadata != nil {
			game.Metadata = metadataToJSONB(req.Metadata)
		}
	} else {
		// Update only masked fields
		for _, path := range req.UpdateMask.Paths {
			switch path {
			case "name":
				game.Name = req.Name
			case "steam_app_id":
				game.SteamAppID = stringPtr(req.SteamAppId)
			case "metadata":
				game.Metadata = metadataToJSONB(req.Metadata)
			}
		}
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
		GameId:   g.GameID,
		Name:     g.Name,
		Metadata: jsonbToMetadata(g.Metadata),
	}

	if g.SteamAppID != nil {
		pbGame.SteamAppId = *g.SteamAppID
	}

	return pbGame
}
