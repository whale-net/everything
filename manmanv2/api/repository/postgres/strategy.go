package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manman"
)

type ConfigurationStrategyRepository struct {
	db *pgxpool.Pool
}

func NewConfigurationStrategyRepository(db *pgxpool.Pool) *ConfigurationStrategyRepository {
	return &ConfigurationStrategyRepository{db: db}
}

func (r *ConfigurationStrategyRepository) Create(ctx context.Context, strategy *manman.ConfigurationStrategy) (*manman.ConfigurationStrategy, error) {
	query := `
		INSERT INTO configuration_strategies (game_id, name, description, strategy_type, target_path, base_template, render_options, apply_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING strategy_id
	`

	err := r.db.QueryRow(ctx, query,
		strategy.GameID,
		strategy.Name,
		strategy.Description,
		strategy.StrategyType,
		strategy.TargetPath,
		strategy.BaseTemplate,
		strategy.RenderOptions,
		strategy.ApplyOrder,
	).Scan(&strategy.StrategyID)
	if err != nil {
		return nil, err
	}

	return strategy, nil
}

func (r *ConfigurationStrategyRepository) Get(ctx context.Context, strategyID int64) (*manman.ConfigurationStrategy, error) {
	strategy := &manman.ConfigurationStrategy{}

	query := `
		SELECT strategy_id, game_id, name, description, strategy_type, target_path, base_template, render_options, apply_order
		FROM configuration_strategies
		WHERE strategy_id = $1
	`

	err := r.db.QueryRow(ctx, query, strategyID).Scan(
		&strategy.StrategyID,
		&strategy.GameID,
		&strategy.Name,
		&strategy.Description,
		&strategy.StrategyType,
		&strategy.TargetPath,
		&strategy.BaseTemplate,
		&strategy.RenderOptions,
		&strategy.ApplyOrder,
	)
	if err != nil {
		return nil, err
	}

	return strategy, nil
}

func (r *ConfigurationStrategyRepository) ListByGame(ctx context.Context, gameID int64) ([]*manman.ConfigurationStrategy, error) {
	query := `
		SELECT strategy_id, game_id, name, description, strategy_type, target_path, base_template, render_options, apply_order
		FROM configuration_strategies
		WHERE game_id = $1
		ORDER BY apply_order, strategy_id
	`

	rows, err := r.db.Query(ctx, query, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var strategies []*manman.ConfigurationStrategy
	for rows.Next() {
		strategy := &manman.ConfigurationStrategy{}
		err := rows.Scan(
			&strategy.StrategyID,
			&strategy.GameID,
			&strategy.Name,
			&strategy.Description,
			&strategy.StrategyType,
			&strategy.TargetPath,
			&strategy.BaseTemplate,
			&strategy.RenderOptions,
			&strategy.ApplyOrder,
		)
		if err != nil {
			return nil, err
		}
		strategies = append(strategies, strategy)
	}

	return strategies, rows.Err()
}

func (r *ConfigurationStrategyRepository) Update(ctx context.Context, strategy *manman.ConfigurationStrategy) error {
	query := `
		UPDATE configuration_strategies
		SET name = $2, description = $3, strategy_type = $4, target_path = $5, base_template = $6, render_options = $7, apply_order = $8, updated_at = CURRENT_TIMESTAMP
		WHERE strategy_id = $1
	`

	_, err := r.db.Exec(ctx, query,
		strategy.StrategyID,
		strategy.Name,
		strategy.Description,
		strategy.StrategyType,
		strategy.TargetPath,
		strategy.BaseTemplate,
		strategy.RenderOptions,
		strategy.ApplyOrder,
	)
	return err
}

func (r *ConfigurationStrategyRepository) Delete(ctx context.Context, strategyID int64) error {
	query := `DELETE FROM configuration_strategies WHERE strategy_id = $1`
	_, err := r.db.Exec(ctx, query, strategyID)
	return err
}
