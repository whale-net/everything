// Mutation success feedback: one toast host in the shell, and the two
// paths a successful mutation's message reaches the operator through
// (FR c97a5018).
//
// The component is krill/ui/components/toast.templ; this file is the
// mechanism that fills it. There are exactly two paths, and which one a
// mutation takes is decided by whether the browser asked for a fragment,
// not by how the handler is written:
//
//   - An htmx request swaps an out-of-band copy of the toast into the
//     host (components.ToastOOB). Out-of-band rather than HX-Trigger
//     deliberately: the toast's markup is built by templ, so the message
//     is escaped by templ's own escaping on the way out, whereas
//     HX-Trigger would hand the raw string to a JavaScript template that
//     would have to escape it a second time.
//   - A non-htmx request is a plain form post, answered with the
//     console's Post/Redirect/Get. A 303 has no body to carry a message,
//     so the message rides a one-shot cookie across the redirect and the
//     landing page renders it as a success alert (components.ToastFlash).
//
// Only a success uses either path. A refusal is not a toast: it is the
// inline htmxui.Alert the intervention path already writes
// (interventions.go's renderInterventionResults carries it in the
// results block), and the toast host is deliberately not a second
// channel for it -- a toast is transient by design, so an outcome
// recorded only there would be gone before a slow operator looked.
package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/krill/ui/components"
)

// toastCookieName carries a success message across a Post/Redirect/Get.
// One shot: renderShellStatus reads it and immediately expires it, so a
// later unrelated page load does not repeat a confirmation the operator
// has already seen.
const toastCookieName = "krill_toast"

// maxToastMessageLen bounds the message the no-JS path will carry in a
// cookie. A cookie has a hard size limit and a confirmation is a single
// line of prose; a longer one is a page-level message that belongs in the
// page, not in a flash.
const maxToastMessageLen = 200

// truncateToastMessage shortens an over-long message at a rune boundary,
// marking it so the operator sees that it was cut rather than silently
// reading a complete-looking sentence that is not.
func truncateToastMessage(message string) string {
	runes := []rune(message)
	if len(runes) <= maxToastMessageLen {
		return message
	}
	return strings.TrimSpace(string(runes[:maxToastMessageLen])) + "…"
}

// withToast returns c followed by an out-of-band toast carrying message.
// An empty (or whitespace-only) message returns c unchanged, so a
// response that names no message shows no toast -- the empty-means-render-
// -nothing rule htmxui §4 states for the primitives, applied here to the
// mechanism that feeds them.
//
// The toast is appended to the fragment rather than swapped in on its own
// because htmx inserts every top-level node of the response: one extra
// response turn would be a round trip whose only content is a
// confirmation the operator is already waiting for.
func withToast(message string, c templ.Component) templ.Component {
	message = strings.TrimSpace(message)
	if message == "" || c == nil {
		return c
	}
	toast := components.ToastOOB(truncateToastMessage(message))
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if err := c.Render(ctx, w); err != nil {
			return err
		}
		return toast.Render(ctx, w)
	})
}

// flashSuccess arms the one-shot cookie the no-JS path reads. Call it on
// the response that redirects away from a successful form post, before
// the redirect is written -- headers set after WriteHeader are dropped.
//
// It sets nothing for an empty message, matching withToast: a mutation
// that names no message names none on either path.
func flashSuccess(w http.ResponseWriter, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     toastCookieName,
		// Base64 rather than url.QueryEscape: the cookie attribute must
		// not gain characters the encoder had to escape, and a decoded
		// message has to survive a round trip through the header intact.
		Value:  base64.RawURLEncoding.EncodeToString([]byte(truncateToastMessage(message))),
		Path:   "/",
		MaxAge: 30,
		// HttpOnly: the message is for the operator to read in the page,
		// never for a script to read and re-render.
		HttpOnly: true,
		// Lax, not Strict: the redirect that completes the POST is a
		// cross-site-initiated top-level navigation as far as the cookie
		// store is concerned, and Strict would drop the message on the one
		// path it exists for.
		SameSite: http.SameSiteLaxMode,
	})
}

// takeFlashSuccess reads and expires the flash cookie, returning the
// message it carried. An absent, unreadable, or empty cookie yields "",
// which every caller renders as nothing.
func takeFlashSuccess(r *http.Request, w http.ResponseWriter) string {
	// Expire unconditionally, before knowing whether the cookie decoded:
	// a message that fails to decode is still a message that was shown
	// once and must not be shown again.
	expireToastCookie(w)

	c, err := r.Cookie(toastCookieName)
	if err != nil {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(decoded))
}

func expireToastCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     toastCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// withFlashSuccess is the page-side half of the no-JS path: it prepends
// the flashed message to a page body as a success alert, or returns the
// body unchanged when there is nothing flashed.
//
// It runs inside renderShellStatus rather than in each page, so every
// shell page gets the message on the post that set it without each page
// having to remember to look for one. It takes the ResponseWriter
// because it expires the cookie, which has to be a header on the very
// response that displays the message.
func withFlashSuccess(r *http.Request, w http.ResponseWriter, body templ.Component) templ.Component {
	message := takeFlashSuccess(r, w)
	if message == "" || body == nil {
		return body
	}
	flash := components.ToastFlash(message)
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if err := flash.Render(ctx, w); err != nil {
			return err
		}
		return body.Render(ctx, w)
	})
}
