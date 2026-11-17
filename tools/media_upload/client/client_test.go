package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/tools/media_upload/uploads"
)

func TestUpload(t *testing.T) {
	var gotBody []byte
	var gotCT string
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer s3.Close()

	var gotAuth string
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "v0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "request_upload"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		Filename  string `json:"filename"`
		SizeBytes int64  `json:"size_bytes"`
		Temporary bool   `json:"temporary"`
	}) (*mcp.CallToolResult, *uploads.Result, error) {
		return nil, &uploads.Result{
			UploadURL: s3.URL + "/k", Method: "PUT", Headers: map[string]string{"Content-Type": "image/png"},
			PublicURL: "https://media.example/" + in.Filename, Key: in.Filename,
		}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		h.ServeHTTP(w, r)
	}))
	defer mcpSrv.Close()

	p := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(p, []byte("pngdata"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Upload(context.Background(), Options{MCPURL: mcpSrv.URL, HTTP: WithBearer("tok"), Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if res.PublicURL != "https://media.example/a.png" || string(gotBody) != "pngdata" || gotCT != "image/png" {
		t.Fatalf("res=%+v body=%q ct=%q", res, gotBody, gotCT)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("auth = %q", gotAuth)
	}
}

func TestUploadRejectsDirectory(t *testing.T) {
	if _, err := Upload(context.Background(), Options{Path: t.TempDir()}); err == nil {
		t.Fatal("expected error for directory")
	}
}
