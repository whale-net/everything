// This file (issue #2545, FR6) is the derived open-question HTTP surface:
// ListOpenQuestionsHandler, GET /design-sessions/{id}/open-questions.
// Ungated, like GetDesignSessionHandler (design_session.go) -- FR3's
// write-only gate applies to no read path in this milestone (root plan
// issue #2485).
package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// OpenQuestionWire is the wire shape of one store.OpenQuestion.
type OpenQuestionWire struct {
	QuestionID    string `json:"question_id"`
	Text          string `json:"text"`
	Blocking      bool   `json:"blocking"`
	OpenedAtSeqNo int    `json:"opened_at_seq_no"`
}

// ListOpenQuestionsResponse is ListOpenQuestionsHandler's response body.
// Exported (issue #2547) so krill/mcp/tools' list_open_questions tool
// returns this exact value -- built by the same NewListOpenQuestionsResponse
// this handler calls -- rather than a second, MCP-local projection of the
// same data (LB7).
type ListOpenQuestionsResponse struct {
	OpenQuestions []OpenQuestionWire `json:"open_questions"`
}

// NewListOpenQuestionsResponse builds a ListOpenQuestionsResponse from
// questions, applying the same onlyBlocking filter ListOpenQuestionsHandler
// applies for its `?blocking=true` query parameter.
func NewListOpenQuestionsResponse(questions []store.OpenQuestion, onlyBlocking bool) ListOpenQuestionsResponse {
	out := make([]OpenQuestionWire, 0, len(questions))
	for _, q := range questions {
		if onlyBlocking && !q.Blocking {
			continue
		}
		out = append(out, OpenQuestionWire{
			QuestionID:    q.QuestionID,
			Text:          q.Text,
			Blocking:      q.Blocking,
			OpenedAtSeqNo: q.OpenedAtSeqNo,
		})
	}
	return ListOpenQuestionsResponse{OpenQuestions: out}
}

// ListOpenQuestionsHandler returns the derived open-question view (FR6):
// GET /design-sessions/{id}/open-questions. 404s on an unknown session id
// (checked via designSessions.GetByID, mirroring GetDesignSessionHandler)
// rather than silently returning an empty list -- an empty list is instead
// exactly what a real, event-free session returns. Supports an optional
// `?blocking=true` filter (FR6: "each tagged blocking or non-blocking").
func ListOpenQuestionsHandler(designSessions store.DesignSessionStore, events store.RevisionEventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		if _, err := designSessions.GetByID(r.Context(), id); err != nil {
			writeDesignSessionNotFoundOrInternalError(w, err)
			return
		}

		questions, err := events.ListOpenQuestions(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to list open questions")
			return
		}

		onlyBlocking := r.URL.Query().Get("blocking") == "true"

		writeJSON(w, http.StatusOK, NewListOpenQuestionsResponse(questions, onlyBlocking))
	}
}
