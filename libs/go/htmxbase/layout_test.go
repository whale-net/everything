package htmxbase

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	tests := []struct {
		name     string
		data     LayoutData
		contains []string
	}{
		{
			name: "basic layout",
			data: LayoutData{
				Title:   "Test Page",
				Content: "<h1>Hello World</h1>",
			},
			contains: []string{
				"<title>Test Page</title>",
				`<link rel="icon" href="/favicon.ico">`,
				"<h1>Hello World</h1>",
				"htmx.org",
				"alpinejs",
			},
		},
		{
			name: "with custom favicon",
			data: LayoutData{
				Title:      "Custom Icon Page",
				FaviconURL: "/static/custom.svg",
				Content:    "<div>Content</div>",
			},
			contains: []string{
				`<link rel="icon" href="/static/custom.svg">`,
			},
		},
		{
			name: "with title suffix",
			data: LayoutData{
				Title:       "Home",
				TitleSuffix: "MyApp",
				Content:     "<div>Content</div>",
			},
			contains: []string{
				"<title>Home - MyApp</title>",
			},
		},
		{
			name: "with custom CSS",
			data: LayoutData{
				Title:     "Styled",
				Content:   "<p>Text</p>",
				CustomCSS: "body { margin: 0; }",
			},
			contains: []string{
				"body { margin: 0; }",
			},
		},
		{
			name: "with custom scripts",
			data: LayoutData{
				Title:         "Interactive",
				Content:       "<button>Click</button>",
				CustomScripts: "console.log('ready');",
			},
			contains: []string{
				"console.log('ready');",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := Render(&buf, tt.data)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}

			output := buf.String()
			for _, want := range tt.contains {
				if !strings.Contains(output, want) {
					t.Errorf("Render() output missing %q", want)
				}
			}
		})
	}
}

// TestRender_HTMX4ScriptAndConfig asserts the htmx 4 core pin and that the
// htmx-config meta (which must be read at htmx load) precedes the script.
func TestRender_HTMX4ScriptAndConfig(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, LayoutData{Title: "T", Content: "<p>x</p>"}); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	output := buf.String()

	const script = "https://unpkg.com/htmx.org@4.0.0/dist/htmx.min.js"
	scriptIdx := strings.Index(output, script)
	if scriptIdx < 0 {
		t.Fatalf("Render() output missing %q", script)
	}
	metaIdx := strings.Index(output, `<meta name="htmx-config"`)
	if metaIdx < 0 {
		t.Fatal(`Render() output missing <meta name="htmx-config"`)
	}
	if metaIdx > scriptIdx {
		t.Errorf("htmx-config meta (idx %d) must appear before the htmx script (idx %d)", metaIdx, scriptIdx)
	}
	metaEnd := strings.Index(output[metaIdx:], ">")
	if !strings.Contains(output[metaIdx:metaIdx+metaEnd], `"noSwap"`) {
		t.Errorf("htmx-config meta must contain \"noSwap\"; got %q", output[metaIdx:metaIdx+metaEnd+1])
	}
}

// TestRender_AlpineCompatLoadsAfterCore asserts the Alpine compat extension
// is present and registered after htmx core (extensions need core loaded).
func TestRender_AlpineCompatLoadsAfterCore(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, LayoutData{Title: "T", Content: "<p>x</p>"}); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	output := buf.String()

	coreIdx := strings.Index(output, "htmx.org@4.0.0/dist/htmx.min.js")
	compatIdx := strings.Index(output, "htmx.org@4.0.0/dist/ext/hx-alpine-compat.min.js")
	if compatIdx < 0 {
		t.Fatal("Render() output missing the hx-alpine-compat extension script")
	}
	if compatIdx < coreIdx {
		t.Errorf("hx-alpine-compat (idx %d) must load after htmx core (idx %d)", compatIdx, coreIdx)
	}
}

func TestRenderError(t *testing.T) {
	// Test that valid data doesn't error
	var buf bytes.Buffer
	err := Render(&buf, LayoutData{
		Title:   "Valid",
		Content: "Content",
	})
	if err != nil {
		t.Errorf("Render() with valid data should not error, got %v", err)
	}
}

func TestFaviconHandler(t *testing.T) {
	dummyIcon := []byte("favicon-bytes-data")

	t.Run("GET request with default content type", func(t *testing.T) {
		handler := FaviconHandler(dummyIcon)
		req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
		rec := httptest.NewRecorder()

		handler(rec, req)

		resp := rec.Result()
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != "image/x-icon" {
			t.Errorf("expected Content-Type image/x-icon, got %q", got)
		}
		if got := resp.Header.Get("Cache-Control"); got != "public, max-age=86400" {
			t.Errorf("expected Cache-Control public, max-age=86400, got %q", got)
		}
		body, _ := io.ReadAll(resp.Body)
		if !bytes.Equal(body, dummyIcon) {
			t.Errorf("expected body %v, got %v", dummyIcon, body)
		}
	})

	t.Run("GET request with custom content type", func(t *testing.T) {
		handler := FaviconHandler(dummyIcon, "image/svg+xml")
		req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
		rec := httptest.NewRecorder()

		handler(rec, req)

		resp := rec.Result()
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != "image/svg+xml" {
			t.Errorf("expected Content-Type image/svg+xml, got %q", got)
		}
	})

	t.Run("HEAD request", func(t *testing.T) {
		handler := FaviconHandler(dummyIcon)
		req := httptest.NewRequest(http.MethodHead, "/favicon.ico", nil)
		rec := httptest.NewRecorder()

		handler(rec, req)

		resp := rec.Result()
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if len(body) != 0 {
			t.Errorf("expected empty body for HEAD request, got %d bytes", len(body))
		}
	})

	t.Run("POST request returns 405 Method Not Allowed", func(t *testing.T) {
		handler := FaviconHandler(dummyIcon)
		req := httptest.NewRequest(http.MethodPost, "/favicon.ico", nil)
		rec := httptest.NewRecorder()

		handler(rec, req)

		resp := rec.Result()
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, resp.StatusCode)
		}
	})
}
