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

// ErrAlreadyImported is returned by Import when rootPath has already been
// imported into the target session's scope (FR12, NFR3, issue #2548): a
// prior import_completion row exists whose SourcePath equals rootPath. The
// check runs before Parse, so a second Import for the same path never
// re-reads the source doc set at all. A genuine re-import is a deliberate
// future operation (see store.ImportCompletion's doc comment), not
// something Import silently retries or absorbs.
var ErrAlreadyImported = errors.New("krill/importer: already imported for this scope and path")

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
//
// sourceRevision is the repo commit SHA rootPath was imported from,
// supplied by the caller (krill/importer/cmd's --source-revision flag) --
// Import itself never shells out to git to discover it. It is recorded
// verbatim on the import_completion row (FR12, NFR3) for the audit trail
// only.
//
// FR12/NFR3's one-time, one-way guarantee: before Parse runs at all,
// Import checks whether rootPath was already imported into this session's
// scope (ErrAlreadyImported below) and refuses if so -- Parse and write
// never even see the source. After write succeeds, Import records the
// completion (store.ImportCompletionStore.MarkComplete) so the next
// Import for the same (scope, path) refuses the same way, and nothing in
// krill ever reads rootPath back off disk afterward.
func Import(ctx context.Context, st *store.Store, sessions store.SessionStore, sessionID uuid.UUID, rootPath, sourceRevision string) (*Report, error) {
	sess, err := requireSession(ctx, sessions, sessionID)
	if err != nil {
		return nil, err
	}

	if err := refuseIfAlreadyImported(ctx, st, sess.ScopeID, rootPath); err != nil {
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

	// FR11 item 2 (issue #2549): attach completeness accounting to the
	// report before it is ever returned, so a caller (cmd/main.go's
	// --allow-unmapped gate) always sees coverage alongside the entity-id
	// list -- never a report that only Render()s the happy-path entries.
	coverage, err := ComputeCoverage(rootPath, parsed)
	if err != nil {
		return nil, fmt.Errorf("import %s: compute coverage: %w", rootPath, err)
	}
	report.Coverage = coverage

	// Not inside write()'s own transaction(s): write() issues one Create
	// call per entity, each in its own transaction (see write.go), so
	// there is no single write-path transaction for this to join. Record
	// completion immediately after write succeeds instead -- if this call
	// fails, the entities above are already committed, but a subsequent
	// Import attempt for the same path will still refuse (the entities'
	// presence alone is not what refuseIfAlreadyImported checks; a failed
	// MarkComplete here does mean FR12's guard would not yet be armed,
	// which is why the report is only returned once this succeeds).
	if _, err := st.ImportCompletions().MarkComplete(ctx, sess.ScopeID, report.ProductID, rootPath, sourceRevision); err != nil {
		return nil, fmt.Errorf("import %s: mark import complete: %w", rootPath, err)
	}

	return report, nil
}

// refuseIfAlreadyImported implements FR12's pre-parse refusal check: it
// looks for any import_completion row in scopeID whose SourcePath equals
// rootPath and, if found, returns ErrAlreadyImported without ever calling
// Parse. The check is scope-and-path keyed rather than
// store.ImportCompletionStore's own (scope, product) key because the
// target Product does not exist -- and is not even known -- until after
// Parse runs and write() resolves/creates it; rootPath is the only stable
// identifier available this early.
func refuseIfAlreadyImported(ctx context.Context, st *store.Store, scopeID uuid.UUID, rootPath string) error {
	completions, err := st.ImportCompletions().ListByScope(ctx, scopeID)
	if err != nil {
		return fmt.Errorf("import: check prior completions for scope %s: %w", scopeID, err)
	}
	for _, c := range completions {
		if c.SourcePath == rootPath {
			return fmt.Errorf("import: %s already imported into scope %s at %s: %w", rootPath, scopeID, c.CompletedAt, ErrAlreadyImported)
		}
	}
	return nil
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
