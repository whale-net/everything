// This file (FR d0a63ffb-8e80-47c1-8a64-2729d4950522) is the
// product-wide design-session aggregate read's HTTP surface: GET
// /products/{id}/design-sessions. Ungated like every other read endpoint in
// this package (FR3's write-only gate, root plan issue #2485) -- the
// caller-supplied {id} is the Product whose scope the read resolves from,
// exactly like GetProductDeliveryHandler's.
//
// LB7 parity: the wire types below are exported and the constructor is
// exported, so krill/mcp/tools' list_product_design_sessions returns this
// exact value rather than an MCP-local mirror of the same data.
package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// DesignSessionSummaryWire is the wire shape of one
// store.DesignSessionSummary. Stage is store.Stage's string value
// verbatim (store/design_session_summary.go owns that vocabulary) so the
// HTTP and MCP surfaces never spell a stage differently from each other.
type DesignSessionSummaryWire struct {
	ID                       string    `json:"id"`
	OpeningSubmission        string    `json:"opening_submission"`
	CreatedAt                time.Time `json:"created_at"`
	Stage                    string    `json:"stage"`
	OpenBlockingQuestions    int       `json:"open_blocking_questions"`
	OpenNonBlockingQuestions int       `json:"open_non_blocking_questions"`
}

// ToDesignSessionSummaryWire converts one store.DesignSessionSummary to
// its wire shape.
func ToDesignSessionSummaryWire(s store.DesignSessionSummary) DesignSessionSummaryWire {
	return DesignSessionSummaryWire{
		ID:                       s.ID.String(),
		OpeningSubmission:        s.OpeningSubmission,
		CreatedAt:                s.CreatedAt,
		Stage:                    string(s.Stage),
		OpenBlockingQuestions:    s.OpenBlockingQuestions,
		OpenNonBlockingQuestions: s.OpenNonBlockingQuestions,
	}
}

// ProductDesignSessionsSummaryWire is the wire shape of one
// store.ProductDesignSessionsSummary: the per-session rows plus the
// product-level blocking totals. Sessions is always non-nil (an empty
// JSON array, never null) so a client can index it without a nil check.
type ProductDesignSessionsSummaryWire struct {
	ProductID                   string                     `json:"product_id"`
	Sessions                    []DesignSessionSummaryWire `json:"sessions"`
	OpenBlockingQuestionCount   int                        `json:"open_blocking_question_count"`
	SessionsHoldingOpenBlocking int                        `json:"sessions_holding_open_blocking"`
}

// NewProductDesignSessionsSummaryWire converts a
// store.ProductDesignSessionsSummary to its wire shape. Exported so
// krill/mcp/tools builds the identical value (LB7).
func NewProductDesignSessionsSummaryWire(s store.ProductDesignSessionsSummary) ProductDesignSessionsSummaryWire {
	out := ProductDesignSessionsSummaryWire{
		ProductID:                   s.ProductID.String(),
		Sessions:                    make([]DesignSessionSummaryWire, len(s.Sessions)),
		OpenBlockingQuestionCount:   s.OpenBlockingQuestionCount,
		SessionsHoldingOpenBlocking: s.SessionsHoldingOpenBlocking,
	}
	for i, row := range s.Sessions {
		out.Sessions[i] = ToDesignSessionSummaryWire(row)
	}
	return out
}

// ListProductDesignSessionsHandler returns the product-wide design-session
// aggregate read: GET /products/{id}/design-sessions -- every session with
// its derived stage and open-question counts, newest first, plus the
// product-level blocking totals, in one request.
//
// 404s on an unknown product id (resolved via the DesignSessionStore's own
// aggregate call, which reports store.ErrNotFound rather than an empty
// listing) so a caller never reads "no sessions yet" where the truth is
// "no such product".
func ListProductDesignSessionsHandler(designSessions store.DesignSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		productID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		summary, err := designSessions.SummarizeByProduct(r.Context(), productID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "product not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, NewProductDesignSessionsSummaryWire(summary))
	}
}
