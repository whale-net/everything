// The mutating routes this binary's own app pages issue: a task
// intervention (escalate) and a design-session submission (open). Every
// one of them goes through withKrillSession, which is the whole point --
// there is exactly one path from a signed-in operator's browser to a krill
// write, and it derives the acting / on-behalf-of Subject from that
// operator's Keycloak session (identity.go) rather than from anything the
// browser sent. The browser's request body carries only the action's own
// arguments; no route here reads an identity, a scope, or a session id
// from the request.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/logging"
)

// logger is this package's shared logger.
var logger = logging.Get("krill/ui")

// errNoOperator is returned by withKrillSession when the request context
// carries no resolved operator Subject -- the route was not mounted behind
// requireOperator, so there is no identity to attribute a write with.
var errNoOperator = errors.New("no resolved operator identity on this request")

// withKrillSession turns the operator Subject requireOperator resolved onto
// ctx into a krill write: it resolves the deployment's scope, mints a krill
// session whose acting and on-behalf-of subjects are both that operator
// (LB4), and calls fn with the session id to issue the write against.
//
// A session is minted per write rather than cached in-process: each
// krill_session row is the durable record of which real identity performed
// which mutation, and a UI's write volume does not make that cost
// interesting. The scope is the one this deployment's seeder guarantees
// (ScopeStore.GetSole), the same resolution an MCP-only caller gets --
// there is exactly one scope row, so a browser has nothing to choose from
// and nothing to be tricked into supplying.
func (app *App) withKrillSession(ctx context.Context, fn func(context.Context, store.SessionID) error) error {
	operator, ok := OperatorSubjectFromContext(ctx)
	if !ok {
		return errNoOperator
	}

	scope, err := app.scopes.GetSole(ctx)
	if err != nil {
		return fmt.Errorf("resolve scope: %w", err)
	}

	sessionID, err := app.writes.InitSession(ctx, scope.ID, operator)
	if err != nil {
		return err
	}
	return fn(ctx, sessionID)
}

// escalateRequest mirrors api's escalate task body (its reason is the only
// argument the verb takes; scope and both subjects come from the session).
type escalateRequest struct {
	Reason *string `json:"reason"`
}

// handleEscalateTask is the UI side of POST /tasks/{id}/escalate: a Swarm
// Operator's manual task intervention. api's response -- the reassembled
// task payload -- is relayed back to the browser unchanged.
func (app *App) handleEscalateTask(w http.ResponseWriter, r *http.Request) {
	taskID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid task id: must be a UUID", http.StatusBadRequest)
		return
	}

	var req escalateRequest
	if err := decodeJSONBody(r, &req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	err = app.withKrillSession(r.Context(), func(ctx context.Context, sessionID store.SessionID) error {
		resp, err := app.writes.Write(ctx, sessionID, http.MethodPost, "tasks/"+taskID.String()+"/escalate", req)
		if err != nil {
			return err
		}
		return relayWriteResponse(w, resp)
	})
	if err != nil {
		writeWriteError(w, err)
	}
}

// openDesignSessionRequest mirrors api's open-design-session body
// (handlers.OpenDesignSessionHandler): a product and the operator's
// opening submission. No identity or scope field exists on either side of
// this call by construction.
type openDesignSessionRequest struct {
	ProductID         string `json:"product_id"`
	OpeningSubmission string `json:"opening_submission"`
}

// handleOpenDesignSession is the UI side of POST /design-sessions: a
// Requirement Contributor's design-session submission, attributed to the
// signed-in operator by the same path every other write here takes.
func (app *App) handleOpenDesignSession(w http.ResponseWriter, r *http.Request) {
	var req openDesignSessionRequest
	if err := decodeJSONBody(r, &req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	err := app.withKrillSession(r.Context(), func(ctx context.Context, sessionID store.SessionID) error {
		resp, err := app.writes.Write(ctx, sessionID, http.MethodPost, "design-sessions", req)
		if err != nil {
			return err
		}
		return relayWriteResponse(w, resp)
	})
	if err != nil {
		writeWriteError(w, err)
	}
}

// decodeJSONBody mirrors api's decodeStrict: an unknown field is a client
// bug worth reporting, not something to ignore.
func decodeJSONBody(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// relayWriteResponse copies api's status and body straight back to the
// browser, so a page shows the same result -- and the same error message --
// it would from a direct api call. A rejected write (unknown task,
// cross-scope product, stale claim) is a normal outcome, not a failure of
// this binary: the status is relayed, nothing is logged above INFO.
func relayWriteResponse(w http.ResponseWriter, resp *http.Response) error {
	defer resp.Body.Close() //nolint:errcheck
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, err := io.Copy(w, resp.Body)
	return err
}

// writeWriteError renders a failure to establish the krill session itself
// (no operator, unresolvable scope, unreachable api). The write never
// reached krill, so nothing is attributed anywhere -- which is the correct
// outcome when the operator's identity cannot be established.
func writeWriteError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNoOperator) {
		http.Error(w, "unresolved operator identity", http.StatusUnauthorized)
		return
	}
	logger.Error("failed to issue an operator write", "error", err)
	http.Error(w, "failed to issue write as the signed-in operator", http.StatusBadGateway)
}
