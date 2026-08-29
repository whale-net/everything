package templadapter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockComponent is a simple mock templ.Component for testing.
type mockComponent struct {
	content string
}

func (m *mockComponent) Render(ctx context.Context, w io.Writer) error {
	_, err := io.WriteString(w, m.content)
	return err
}

// mockFailingComponent is a templ.Component that always fails to render.
type mockFailingComponent struct {
	err error
}

func (m *mockFailingComponent) Render(ctx context.Context, w io.Writer) error {
	return m.err
}

// TestAdapt_TrivialComponent tests that a trivial templ component
// is adapted and produces the expected bytes for a given request/topic.
func TestAdapt_TrivialComponent(t *testing.T) {
	// Create a simple component that renders known content
	expectedContent := "<div>Hello, World!</div>"
	componentFunc := func(r *http.Request, topic string) templ.Component {
		return &mockComponent{content: expectedContent}
	}

	// Adapt the component function
	fragment := Adapt(componentFunc)

	// Call the fragment with a mock request and topic
	req := httptest.NewRequest("GET", "/test", nil)
	topic := "test-topic"

	result, err := fragment(req, topic)

	// Verify no error and content matches
	require.NoError(t, err, "Adapt should not return an error for a successful render")
	assert.Equal(t, []byte(expectedContent), result, "Adapt should produce the expected bytes")
}

// TestAdapt_RenderError tests that a component whose render fails
// produces an error that is treated as transient by the adapter.
func TestAdapt_RenderError(t *testing.T) {
	// Create a component that fails to render
	renderErr := errors.New("component render failed")
	componentFunc := func(r *http.Request, topic string) templ.Component {
		return &mockFailingComponent{err: renderErr}
	}

	// Adapt the component function
	fragment := Adapt(componentFunc)

	// Call the fragment with a mock request and topic
	req := httptest.NewRequest("GET", "/test", nil)
	topic := "test-topic"

	result, err := fragment(req, topic)

	// Verify that the error is propagated and no bytes are written
	assert.Equal(t, renderErr, err, "Adapt should propagate the render error")
	assert.Nil(t, result, "Adapt should return nil bytes when render fails")
}
