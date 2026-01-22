package repository

import (
	"context"

	"github.com/whale-net/everything/manman"
)

// ServerRepository defines operations for Server entities
type ServerRepository interface {
	Create(ctx context.Context, name string) (*manman.Server, error)
	Get(ctx context.Context, serverID int64) (*manman.Server, error)
	List(ctx context.Context, limit, offset int) ([]*manman.Server, error)
	Update(ctx context.Context, server *manman.Server) error
	Delete(ctx context.Context, serverID int64) error
}

// GameRepository defines operations for Game entities
type GameRepository interface {
	Create(ctx context.Context, game *manman.Game) (*manman.Game, error)
	Get(ctx context.Context, gameID int64) (*manman.Game, error)
	List(ctx context.Context, limit, offset int) ([]*manman.Game, error)
	Update(ctx context.Context, game *manman.Game) error
	Delete(ctx context.Context, gameID int64) error
}

// GameConfigRepository defines operations for GameConfig entities
type GameConfigRepository interface {
	Create(ctx context.Context, config *manman.GameConfig) (*manman.GameConfig, error)
	Get(ctx context.Context, configID int64) (*manman.GameConfig, error)
	List(ctx context.Context, gameID *int64, limit, offset int) ([]*manman.GameConfig, error)
	Update(ctx context.Context, config *manman.GameConfig) error
	Delete(ctx context.Context, configID int64) error
}

// ServerGameConfigRepository defines operations for ServerGameConfig entities
type ServerGameConfigRepository interface {
	Create(ctx context.Context, sgc *manman.ServerGameConfig) (*manman.ServerGameConfig, error)
	Get(ctx context.Context, sgcID int64) (*manman.ServerGameConfig, error)
	List(ctx context.Context, serverID *int64, limit, offset int) ([]*manman.ServerGameConfig, error)
	Update(ctx context.Context, sgc *manman.ServerGameConfig) error
	Delete(ctx context.Context, sgcID int64) error
}

// SessionRepository defines operations for Session entities
type SessionRepository interface {
	Create(ctx context.Context, session *manman.Session) (*manman.Session, error)
	Get(ctx context.Context, sessionID int64) (*manman.Session, error)
	List(ctx context.Context, sgcID *int64, limit, offset int) ([]*manman.Session, error)
	Update(ctx context.Context, session *manman.Session) error
}

// Repository aggregates all repository interfaces
type Repository struct {
	Servers           ServerRepository
	Games             GameRepository
	GameConfigs       GameConfigRepository
	ServerGameConfigs ServerGameConfigRepository
	Sessions          SessionRepository
}
