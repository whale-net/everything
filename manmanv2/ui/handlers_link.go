package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/whagentlink"
	"github.com/whale-net/everything/manmanv2/identitylink"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// Outcome values carried back to whagent-net's /link/manmanv2/result.
const (
	linkOutcomeLinked        = "linked"
	linkOutcomeAlreadyLinked = "already_linked"
	linkOutcomeConflict      = "conflict"
	linkOutcomeRejected      = "rejected"
)

const linkCompletePath = "/link/whagent/complete"

type linkVerifier interface {
	Verify(ctx context.Context, token string) (*whagentlink.Assertion, error)
}

type linkStore interface {
	Link(ctx context.Context, iss, sub, userSub string) (identitylink.Outcome, error)
	IsConsumed(ctx context.Context, jti string) (bool, error)
	Consume(ctx context.Context, jti string, expiresAt time.Time) error
}

// grantStarter sends a user with no stored MCP grant through Keycloak consent
// (grantflow.Consent.Begin), so the MCP can act as them once linked.
type grantStarter interface {
	Begin(w http.ResponseWriter, r *http.Request, returnTo string) (bool, error)
}

// whagentLinkHandlers link a whagent-net operator's identity to the signed-in
// manmanv2 user. whagentURL is whagent-net's public URL (the assertion issuer).
type whagentLinkHandlers struct {
	verifier   linkVerifier
	store      linkStore
	grants     grantStarter
	whagentURL string
}

func (h *whagentLinkHandlers) logger() *slog.Logger { return logging.Get("manmanv2/ui/link") }

func (h *whagentLinkHandlers) layout(r *http.Request, title string) components.LayoutData {
	return components.LayoutData{Title: title, User: htmxauth.GetUser(r.Context())}
}

// verify checks token, its return-URL origin, and replay state. On failure it
// has already responded.
func (h *whagentLinkHandlers) verify(w http.ResponseWriter, r *http.Request, token string) *whagentlink.Assertion {
	ctx := r.Context()
	if token == "" {
		h.logger().WarnContext(ctx, "link assertion rejected: no token")
		h.reject(w, r, token)
		return nil
	}
	a, err := h.verifier.Verify(ctx, token)
	if err != nil {
		h.logger().WarnContext(ctx, "link assertion rejected: verification failed", "error", err)
		h.reject(w, r, token)
		return nil
	}
	if !whagentlink.ReturnURLOriginMatches(a.ReturnURL, h.whagentURL) {
		h.logger().WarnContext(ctx, "link assertion rejected: return url origin mismatch")
		h.renderRejected(w, r)
		return nil
	}
	consumed, err := h.store.IsConsumed(ctx, a.ID)
	if err != nil {
		h.logger().ErrorContext(ctx, "link replay check failed", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return nil
	}
	if consumed {
		h.logger().WarnContext(ctx, "link assertion rejected: already consumed", "jti", a.ID)
		h.redirectOutcome(w, r, a.ReturnURL, linkOutcomeRejected)
		return nil
	}
	return a
}

// handleShow is GET /link/whagent: shows the confirmation for a valid assertion.
func (h *whagentLinkHandlers) handleShow(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	a := h.verify(w, r, token)
	if a == nil {
		return
	}
	user := htmxauth.GetUser(r.Context())
	data := pages.WhagentLinkConfirmData{
		Layout:       h.layout(r, "Link whagent-net identity"),
		UserLabel:    userLabel(user),
		SubjectLabel: a.Subject + " (" + a.SubjectIssuer + ")",
		Token:        token,
	}
	if err := RenderTempl(w, r, data.Layout.Title, pages.WhagentLinkConfirm(data)); err != nil {
		h.logger().ErrorContext(r.Context(), "render link confirmation failed", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleConfirm is POST /link/whagent/confirm: consumes the assertion, writes
// the mapping, makes sure the user has an MCP grant, and returns to whagent-net.
func (h *whagentLinkHandlers) handleConfirm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := htmxauth.GetUser(ctx)
	if user == nil || user.Sub == "" {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	if !sameOriginPost(r) {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	a := h.verify(w, r, r.FormValue("token"))
	if a == nil {
		return
	}
	if err := h.store.Consume(ctx, a.ID, a.Expiry); err != nil {
		if errors.Is(err, identitylink.ErrAssertionConsumed) {
			h.redirectOutcome(w, r, a.ReturnURL, linkOutcomeRejected)
			return
		}
		h.logger().ErrorContext(ctx, "consume link assertion failed", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	outcome := linkOutcomeLinked
	res, err := h.store.Link(ctx, a.SubjectIssuer, a.Subject, user.Sub)
	switch {
	case errors.Is(err, identitylink.ErrLinkedToOtherUser):
		h.logger().WarnContext(ctx, "link rejected: whagent identity already linked to a different user", "iss", a.SubjectIssuer, "sub", a.Subject)
		h.redirectOutcome(w, r, a.ReturnURL, linkOutcomeConflict)
		return
	case err != nil:
		h.logger().ErrorContext(ctx, "write whagent identity link failed", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	case res == identitylink.AlreadyLinked:
		outcome = linkOutcomeAlreadyLinked
	}
	h.logger().InfoContext(ctx, "whagent-net identity linked", "iss", a.SubjectIssuer, "sub", a.Subject, "outcome", outcome)

	// The MCP needs a stored grant to act as this user; consent once if missing.
	resume := linkCompletePath + "?" + url.Values{"return": {a.ReturnURL}, "outcome": {outcome}}.Encode()
	started, err := h.grants.Begin(w, r, resume)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !started {
		h.redirectOutcome(w, r, a.ReturnURL, outcome)
	}
}

// handleComplete is GET /link/whagent/complete: resumes the return to
// whagent-net after the one-time Keycloak consent.
func (h *whagentLinkHandlers) handleComplete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	outcome := q.Get("outcome")
	if !whagentlink.ReturnURLOriginMatches(q.Get("return"), h.whagentURL) ||
		(outcome != linkOutcomeLinked && outcome != linkOutcomeAlreadyLinked) {
		h.renderRejected(w, r)
		return
	}
	h.redirectOutcome(w, r, q.Get("return"), outcome)
}

func (h *whagentLinkHandlers) reject(w http.ResponseWriter, r *http.Request, token string) {
	if ret, ok := whagentlink.UnverifiedReturnURL(token); ok && whagentlink.ReturnURLOriginMatches(ret, h.whagentURL) {
		h.redirectOutcome(w, r, ret, linkOutcomeRejected)
		return
	}
	h.renderRejected(w, r)
}

func (h *whagentLinkHandlers) renderRejected(w http.ResponseWriter, r *http.Request) {
	layout := h.layout(r, "Link request rejected")
	if err := RenderTempl(w, r, layout.Title, pages.WhagentLinkRejected(layout)); err != nil {
		h.logger().ErrorContext(r.Context(), "render link rejection failed", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (h *whagentLinkHandlers) redirectOutcome(w http.ResponseWriter, r *http.Request, returnURL, outcome string) {
	u, err := url.Parse(returnURL)
	if err != nil {
		http.Error(w, "invalid return url", http.StatusInternalServerError)
		return
	}
	q := u.Query()
	q.Set("outcome", outcome)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

// sameOriginPost refuses a cross-site form post: a stranger's valid assertion
// must not be linkable to a victim's session without the victim seeing it.
func sameOriginPost(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	}
	return true
}

func userLabel(u *htmxauth.UserInfo) string {
	if u == nil {
		return ""
	}
	for _, v := range []string{u.Name, u.PreferredUsername, u.Email, u.Sub} {
		if v != "" {
			return v
		}
	}
	return ""
}
