package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type idempotencyRepo struct{ ex dbtx }

func (r *idempotencyRepo) Get(ctx context.Context, key, method string) ([]byte, bool, error) {
	var response []byte
	err := r.ex.QueryRow(ctx, `SELECT response_bytes FROM idempotency_key WHERE idempotency_key = $1 AND method = $2`, key, method).Scan(&response)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return response, true, nil
}

func (r *idempotencyRepo) Put(ctx context.Context, key, method string, response []byte) error {
	_, err := r.ex.Exec(ctx, `
		INSERT INTO idempotency_key (idempotency_key, method, response_bytes)
		VALUES ($1, $2, $3)
		ON CONFLICT (idempotency_key, method) DO NOTHING`,
		key, method, response)
	return err
}
