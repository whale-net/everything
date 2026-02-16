package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2"
)

type GameRepository struct {
	db *pgxpool.Pool
}

func NewGameRepository(db *pgxpool.Pool) *GameRepository {
	return &GameRepository{db: db}
}

func (r *GameRepository) Create(ctx context.Context, game *manman.Game) (*manman.Game, error) {
	query := `
		INSERT INTO games (name, steam_app_id, metadata)
		VALUES ($1, $2, $3)
		RETURNING game_id
	`

	err := r.db.QueryRow(ctx, query, game.Name, game.SteamAppID, game.Metadata).Scan(&game.GameID)
	if err != nil {
		return nil, err
	}

	return game, nil
}

func (r *GameRepository) Get(ctx context.Context, gameID int64) (*manman.Game, error) {
	game := &manman.Game{}

	query := `
		SELECT game_id, name, steam_app_id, metadata
		FROM games
		WHERE game_id = $1
	`

	err := r.db.QueryRow(ctx, query, gameID).Scan(
		&game.GameID,
		&game.Name,
		&game.SteamAppID,
		&game.Metadata,
	)
	if err != nil {
		return nil, err
	}

	return game, nil
}

func (r *GameRepository) List(ctx context.Context, limit, offset int) ([]*manman.Game, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT game_id, name, steam_app_id, metadata
		FROM games
		ORDER BY game_id
		LIMIT $1 OFFSET $2
	`

	rows, err := r.db.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var games []*manman.Game
	for rows.Next() {
		game := &manman.Game{}
		err := rows.Scan(
			&game.GameID,
			&game.Name,
			&game.SteamAppID,
			&game.Metadata,
		)
		if err != nil {
			return nil, err
		}
		games = append(games, game)
	}

	return games, rows.Err()
}

func (r *GameRepository) Update(ctx context.Context, game *manman.Game) error {
	query := `
		UPDATE games
		SET name = $2, steam_app_id = $3, metadata = $4
		WHERE game_id = $1
	`

	_, err := r.db.Exec(ctx, query, game.GameID, game.Name, game.SteamAppID, game.Metadata)
	return err
}

func (r *GameRepository) Delete(ctx context.Context, gameID int64) error {
	query := `DELETE FROM games WHERE game_id = $1`
	_, err := r.db.Exec(ctx, query, gameID)
	return err
}
