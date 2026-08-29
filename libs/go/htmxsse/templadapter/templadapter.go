package templadapter

import (
	"bytes"
	"context"
	"net/http"

	"github.com/a-h/templ"
	"github.com/whale-net/everything/libs/go/htmxsse"
)

// ComponentFunc is a function that produces a templ.Component for a given request and topic.
type ComponentFunc func(*http.Request, string) templ.Component

// Adapt converts a templ.Component-producing function into an htmxsse Fragment.
// The resulting Fragment renders the component to bytes and returns any render error
// as an ordinary FR3 error (transient, non-signalling).
func Adapt(fn ComponentFunc) htmxsse.Fragment {
	return func(r *http.Request, topic string) ([]byte, error) {
		component := fn(r, topic)
		var buf bytes.Buffer
		if err := component.Render(context.Background(), &buf); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
}
