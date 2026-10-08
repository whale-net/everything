// Mutating routes issued by the app's own pages. All go through
// withKrillSession, which derives the acting Subject from the operator's
// session; no route reads identity, scope, or session id from the request.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/logging"
)

var logger = logging.Get("krill/ui")

// errNoOperator means the route was not mounted behind requireOperator.
var errNoOperator = errors.New("no resolved operator identity on this request")

// withKrillSession mints a krill session acting as, and on behalf of, the
// resolved operator in the deployment's sole scope, and calls fn with it.
// A session per write keeps each krill_session row a durable attribution record.
func (app *App) withKrillSession(ctx context.Context, fn func(context.Context, store.SessionID) error) error {
	if _, ok := OperatorSubjectFromContext(ctx); !ok {
		return errNoOperator
	}

	sessionID, err := app.writes.InitSession(ctx)
	if err != nil {
		return err
	}
	return fn(ctx, sessionID)
}

// escalateRequest mirrors api's escalate body; scope and subjects come from
// the session.
type escalateRequest struct {
	Reason *string `json:"reason"`
}

// handleEscalateTask proxies an operator's task escalation and relays api's
// response unchanged.
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

// openDesignSessionRequest mirrors api's open-design-session body; it has no
// identity or scope field by construction.
type openDesignSessionRequest struct {
	ProductID         string `json:"product_id"`
	OpeningSubmission string `json:"opening_submission"`
}

// handleOpenDesignSession proxies a design-session submission attributed to
// the signed-in operator.
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

// decodeJSONBody rejects unknown fields, matching api's decodeStrict.
func decodeJSONBody(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// relayWriteResponse copies api's status and body back to the browser. A
// rejected write is a normal outcome, so nothing is logged above INFO.
func relayWriteResponse(w http.ResponseWriter, resp *http.Response) error {
	defer resp.Body.Close() //nolint:errcheck
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, err := io.Copy(w, resp.Body)
	return err
}

// writeWriteError renders a failure to establish the krill session; the
// write never reached krill.
func writeWriteError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNoOperator) {
		http.Error(w, "unresolved operator identity", http.StatusUnauthorized)
		return
	}
	var rejection *writeRejection
	if errors.As(err, &rejection) {
		http.Error(w, rejection.message, rejection.status)
		return
	}
	logger.Error("failed to issue an operator write", "error", err)
	http.Error(w, "failed to issue write as the signed-in operator", http.StatusBadGateway)
}
