// Package client uploads a local file through the media-upload MCP server:
// it calls request_upload, then PUTs the bytes to the returned presigned URL.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/tools/media_upload/uploads"
)

// Options configures one upload.
type Options struct {
	MCPURL    string       // media-upload MCP endpoint
	HTTP      *http.Client // carries the bearer token for MCPURL; nil = default
	Path      string
	Temporary bool
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

// WithBearer returns an HTTP client that adds an Authorization header.
func WithBearer(token string) *http.Client {
	return &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}}
}

// Upload uploads opts.Path and returns the issued result (PublicURL is the
// embeddable link).
func Upload(ctx context.Context, opts Options) (*uploads.Result, error) {
	f, err := os.Open(opts.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", opts.Path)
	}

	res, err := requestUpload(ctx, opts, filepath.Base(opts.Path), st.Size())
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, res.Method, res.UploadURL, f)
	if err != nil {
		return nil, err
	}
	req.ContentLength = st.Size()
	for k, v := range res.Headers {
		req.Header.Set(k, v)
	}
	// The presigned URL carries its own signature; send no bearer token.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("upload: %s: %s", resp.Status, body)
	}
	return res, nil
}

func requestUpload(ctx context.Context, opts Options, name string, size int64) (*uploads.Result, error) {
	c := mcp.NewClient(&mcp.Implementation{Name: "media-upload-cli", Version: "v0"}, nil)
	sess, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: opts.MCPURL, HTTPClient: opts.HTTP}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", opts.MCPURL, err)
	}
	defer sess.Close()

	out, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name:      "request_upload",
		Arguments: map[string]any{"filename": name, "size_bytes": size, "temporary": opts.Temporary},
	})
	if err != nil {
		return nil, fmt.Errorf("request_upload: %w", err)
	}
	if out.IsError {
		return nil, fmt.Errorf("request_upload: %s", textOf(out))
	}
	b, err := json.Marshal(out.StructuredContent)
	if err != nil {
		return nil, err
	}
	var res uploads.Result
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("decode request_upload result: %w", err)
	}
	if res.UploadURL == "" || res.PublicURL == "" {
		return nil, fmt.Errorf("request_upload returned no upload_url/public_url")
	}
	return &res, nil
}

func textOf(r *mcp.CallToolResult) string {
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			return t.Text
		}
	}
	return "unknown error"
}
