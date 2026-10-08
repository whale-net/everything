// Mutation success toasts. htmx requests get an out-of-band toast (escaped by
// templ); plain form posts carry the message in a one-shot cookie across the
// Post/Redirect/Get. Refusals use inline alerts, never toasts.
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

// toastCookieName carries a success message across a redirect; it is expired
// as soon as it is read so it shows once.
const toastCookieName = "krill_toast"

// maxToastMessageLen bounds a message so it fits in a cookie.
const maxToastMessageLen = 200

// truncateToastMessage cuts at a rune boundary and marks the cut.
func truncateToastMessage(message string) string {
	runes := []rune(message)
	if len(runes) <= maxToastMessageLen {
		return message
	}
	return strings.TrimSpace(string(runes[:maxToastMessageLen])) + "…"
}

// withToast appends an out-of-band toast to c; an empty message returns c
// unchanged. Appending avoids a second round trip just for the toast.
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

// flashSuccess arms the no-JS flash cookie. Call it before the redirect is
// written; headers set after WriteHeader are dropped.
func flashSuccess(w http.ResponseWriter, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: toastCookieName,
		// Base64 keeps the cookie value free of characters needing escaping.
		Value:    base64.RawURLEncoding.EncodeToString([]byte(truncateToastMessage(message))),
		Path:     "/",
		MaxAge:   30,
		HttpOnly: true,
		// Lax, not Strict: Strict would drop the cookie on the redirect it exists for.
		SameSite: http.SameSiteLaxMode,
	})
}

// takeFlashSuccess reads and expires the flash cookie; any failure yields "".
func takeFlashSuccess(r *http.Request, w http.ResponseWriter) string {
	c, err := r.Cookie(toastCookieName)
	if err != nil {
		return ""
	}
	// Expire before decoding so an undecodable message is not retried, but only
	// when the cookie exists so ordinary loads send no Set-Cookie.
	expireToastCookie(w)

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

// withFlashSuccess prepends a flashed message as a success alert. It runs in
// renderShellStatus so every shell page shows it and the expiry header lands on
// the displaying response.
func withFlashSuccess(r *http.Request, w http.ResponseWriter, body templ.Component) templ.Component {
	// A fragment would swap the alert into its target and consume the flash;
	// leave the cookie for a full page load.
	if isHtmxRequest(r) {
		return body
	}
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
