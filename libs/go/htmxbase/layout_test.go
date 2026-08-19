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
