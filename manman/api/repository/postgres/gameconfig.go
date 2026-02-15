package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manman"
)

type GameConfigRepository struct {
	db *pgxpool.Pool
}

func NewGameConfigRepository(db *pgxpool.Pool) *GameConfigRepository {
	return &GameConfigRepository{db: db}
}

func (r *GameConfigRepository) Create(ctx context.Context, config *manman.GameConfig) (*manman.GameConfig, error) {
	query := `
		INSERT INTO game_configs (game_id, name, image, args_template, env_template, files, entrypoint, command)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING config_id
	`

	err := r.db.QueryRow(ctx, query,
		config.GameID,
		config.Name,
		config.Image,
		config.ArgsTemplate,
		config.EnvTemplate,
		config.Files,
		config.Entrypoint,
		config.Command,
	).Scan(&config.ConfigID)
	if err != nil {
		return nil, err
	}

	return config, nil
}

func (r *GameConfigRepository) Get(ctx context.Context, configID int64) (*manman.GameConfig, error) {
	config := &manman.GameConfig{}

	query := `
		SELECT config_id, game_id, name, image, args_template, env_template, files, entrypoint, command
		FROM game_configs
		WHERE config_id = $1
	`

	err := r.db.QueryRow(ctx, query, configID).Scan(
		&config.ConfigID,
		&config.GameID,
		&config.Name,
		&config.Image,
		&config.ArgsTemplate,
		&config.EnvTemplate,
		&config.Files,
		&config.Entrypoint,
		&config.Command,
	)
	if err != nil {
		return nil, err
	}

	return config, nil
}

func (r *GameConfigRepository) List(ctx context.Context, gameID *int64, limit, offset int) ([]*manman.GameConfig, error) {
	if limit <= 0 {
		limit = 50
	}

	var query string
	var args []interface{}

	if gameID != nil {
		query = `
			SELECT config_id, game_id, name, image, args_template, env_template, files, entrypoint, command
			FROM game_configs
			WHERE game_id = $1
			ORDER BY config_id
			LIMIT $2 OFFSET $3
		`
		args = []interface{}{*gameID, limit, offset}
	} else {
		query = `
			SELECT config_id, game_id, name, image, args_template, env_template, files, entrypoint, command
			FROM game_configs
			ORDER BY config_id
			LIMIT $1 OFFSET $2
		`
		args = []interface{}{limit, offset}
	}

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []*manman.GameConfig
	for rows.Next() {
		config := &manman.GameConfig{}
		err := rows.Scan(
			&config.ConfigID,
			&config.GameID,
			&config.Name,
			&config.Image,
			&config.ArgsTemplate,
			&config.EnvTemplate,
			&config.Files,
			&config.Entrypoint,
			&config.Command,
		)
		if err != nil {
			return nil, err
		}
		configs = append(configs, config)
	}

	return configs, rows.Err()
}

func (r *GameConfigRepository) Update(ctx context.Context, config *manman.GameConfig) error {
	query := `
		UPDATE game_configs
		SET name = $2, image = $3, args_template = $4, env_template = $5, files = $6, entrypoint = $7, command = $8
		WHERE config_id = $1
	`

	_, err := r.db.Exec(ctx, query,
		config.ConfigID,
		config.Name,
		config.Image,
		config.ArgsTemplate,
		config.EnvTemplate,
		config.Files,
		config.Entrypoint,
		config.Command,
	)
	return err
}

func (r *GameConfigRepository) Delete(ctx context.Context, configID int64) error {
	query := `DELETE FROM game_configs WHERE config_id = $1`
	_, err := r.db.Exec(ctx, query, configID)
	return err
}
