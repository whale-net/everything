package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// domainAdoptionRepo implements repository.DomainAdoptionRepository over
// the `domain_adoption` table (migration 001).
type domainAdoptionRepo struct{ ex dbtx }

func (r *domainAdoptionRepo) GetStage(ctx context.Context, domain string) (repository.DomainAdoptionStage, error) {
	var stage string
	err := r.ex.QueryRow(ctx, `SELECT stage FROM domain_adoption WHERE domain = $1`, domain).Scan(&stage)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row means the domain has never been cut over from the
		// implicit default -- see the interface doc comment on GetStage.
		return repository.DomainAdoptionStageObserve, nil
	}
	if err != nil {
		return "", err
	}
	return repository.DomainAdoptionStage(stage), nil
}
