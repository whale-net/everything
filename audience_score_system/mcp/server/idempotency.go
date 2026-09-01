// Idempotency middleware -- Scaffold-only skeleton for issue #1575's
// Implementation phase. Every write tool's schema includes an optional
// idempotency_key string; once wired into RegisterWrite (registry.go), a
// call carrying one must run its handler under store.Idempotency.Do's
// (tool, personID, key) guard (NFR2/LB4) -- computed here from
// RunIdempotent, the only sanctioned entry point once wired. A tool with
// no key must instead be safe via natural-key upsert; this task's
// Implementation section requires every write tool to state which
// mechanism it uses.
package server

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/whale-net/everything/audience_score_system/store"
)

// RunIdempotent runs fn under idempotency's (tool, personID, key) guard --
// a thin pass-through to store.Idempotency.Do (already real, migration
// 002/#1569). Scaffold only -- not yet called from RegisterWrite; issue
// #1575's Implementation phase applies this to every write tool call that
// carries an idempotency_key, computing fingerprint from a stable hash of
// the tool name plus normalized arguments.
func RunIdempotent(ctx context.Context, idempotency store.Idempotency, tool string, personID uuid.UUID, key, fingerprint string, fn func(context.Context) (uuid.UUID, error)) (uuid.UUID, bool, error) {
	if idempotency == nil {
		return uuid.Nil, false, errors.New("idempotency store not configured")
	}
	return idempotency.Do(ctx, tool, personID, key, fingerprint, fn)
}
