package htmxbase

import (
	"bytes"
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
				"<h1>Hello World</h1>",
				"htmx.org",
				"alpinejs",
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
