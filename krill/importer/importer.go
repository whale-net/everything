// Package importer is the one-way markdown importer (issue #2492, FR16,
// FR17): it parses a `PRODUCT.md` + `product/*.md` doc set (the layout
// tools/project-manager/CONVENTIONS.md § Layout defines) into krill's spec
// entities and reports what became what.
//
// One-way constraint (LB5, FR15): this is the *only* code path in `krill/`
// that ever parses a committed markdown document back into entities.
// PRODUCT.md's LB5 states the direction of ownership explicitly -- krill's
// surrogate id is an FR's identity, a rendered file is a projection with
// provenance, and "nothing ever parses a committed doc back into krill
// except the one-time importer (C8), which is a separate, deliberately
// one-way code path with no reverse." Do not add a second reader of a
// committed doc anywhere else in this repo's krill/ tree -- a renderer
// (FR13, a separate task) only ever writes markdown from krill's entities,
// never the other direction, and this package itself never writes markdown.
package importer

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// Import is the gated, database-writing entrypoint (FR16/FR17): it
// requires sessionID to name a session `init` (FR3) actually minted --
// import is one of the six write paths RequireSession's doc comment
// (krill/api/handlers/gate.go) names -- then parses rootPath (Parse) and
// writes every entity found into st under the session's scope (write),
// returning the entity-id report.
//
// Import is a CLI entrypoint (krill/importer/cmd), not an HTTP handler, so
// it cannot literally wrap itself in api/handlers.RequireSession -- that
// middleware needs an http.Handler to wrap. requireSession below performs
// the same check RequireSession does (resolve sessionID against
// store.SessionStore.GetSession, reject if unknown) directly against the
// store, so a caller with no valid krill session cannot import regardless
// of which front door they come through.
func Import(ctx context.Context, st *store.Store, sessions store.SessionStore, sessionID uuid.UUID, rootPath string) (*Report, error) {
	sess, err := requireSession(ctx, sessions, sessionID)
	if err != nil {
		return nil, err
	}

	parsed, err := Parse(rootPath)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rootPath, err)
	}

	report, err := write(ctx, st, sess.ScopeID, parsed)
	if err != nil {
		return nil, fmt.Errorf("import %s: %w", rootPath, err)
	}
	return report, nil
}

// requireSession resolves sessionID against sessions, mirroring
// api/handlers.RequireSession's write gate (FR3) for a caller that is not
// going through HTTP. It returns the resolved store.Session so the caller
// can attribute its writes to the session's scope, exactly as
// handlers.GatedSession does for an HTTP write handler.
func requireSession(ctx context.Context, sessions store.SessionStore, sessionID uuid.UUID) (store.Session, error) {
	sess, err := sessions.GetSession(ctx, store.SessionID(sessionID))
	if errors.Is(err, store.ErrSessionNotFound) {
		return store.Session{}, fmt.Errorf("import: unknown krill session %s: %w", sessionID, err)
	}
	if err != nil {
		return store.Session{}, fmt.Errorf("import: resolve krill session %s: %w", sessionID, err)
	}
	return sess, nil
}
