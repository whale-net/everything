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

// openQuestionWire is the wire shape of one store.OpenQuestion.
type openQuestionWire struct {
	QuestionID    string `json:"question_id"`
	Text          string `json:"text"`
	Blocking      bool   `json:"blocking"`
	OpenedAtSeqNo int    `json:"opened_at_seq_no"`
}

// listOpenQuestionsResponse is ListOpenQuestionsHandler's response body.
type listOpenQuestionsResponse struct {
	OpenQuestions []openQuestionWire `json:"open_questions"`
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

		out := make([]openQuestionWire, 0, len(questions))
		for _, q := range questions {
			if onlyBlocking && !q.Blocking {
				continue
			}
			out = append(out, openQuestionWire{
				QuestionID:    q.QuestionID,
				Text:          q.Text,
				Blocking:      q.Blocking,
				OpenedAtSeqNo: q.OpenedAtSeqNo,
			})
		}

		writeJSON(w, http.StatusOK, listOpenQuestionsResponse{OpenQuestions: out})
	}
}
