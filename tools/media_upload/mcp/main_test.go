package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/tools/media_upload/uploads"
)

type fakeVerifier struct {
	claims *grpcauth.Claims
	err    error
}

func (f fakeVerifier) Verify(context.Context, string) (*grpcauth.Claims, error) {
	return f.claims, f.err
}

func TestHasRole(t *testing.T) {
	roles := []string{"viewer", "uploader"}
	if !hasRole(roles, "uploader") {
		t.Error("hasRole(uploader) = false, want true")
	}
	if hasRole(roles, "admin") {
		t.Error("hasRole(admin) = true, want false")
	}
	if hasRole(nil, "uploader") {
		t.Error("hasRole(nil, uploader) = true, want false")
	}
}

func TestEnvInt(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		def     int
		want    int
		wantErr bool
	}{
		{name: "unset uses default", value: "", def: 50, want: 50},
		{name: "valid", value: "25", def: 50, want: 25},
		{name: "non-numeric", value: "abc", def: 50, wantErr: true},
		{name: "zero rejected", value: "0", def: 50, wantErr: true},
		{name: "negative rejected", value: "-3", def: 50, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MEDIA_TEST_INT", tc.value)
			got, err := envInt("MEDIA_TEST_INT", tc.def)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestOIDCTokenVerifier(t *testing.T) {
	ctx := context.Background()

	t.Run("valid without required role", func(t *testing.T) {
		v := oidcTokenVerifier(fakeVerifier{claims: &grpcauth.Claims{Subject: "user-1"}}, "")
		info, err := v(ctx, "tok", nil)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if info.UserID != "user-1" || info.Extra[subjectKey] != "user-1" {
			t.Fatalf("info = %+v", info)
		}
	})

	t.Run("required role present", func(t *testing.T) {
		v := oidcTokenVerifier(fakeVerifier{claims: &grpcauth.Claims{Subject: "user-2", Roles: []string{"uploader"}}}, "uploader")
		info, err := v(ctx, "tok", nil)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if info.UserID != "user-2" {
			t.Fatalf("UserID = %q", info.UserID)
		}
	})

	t.Run("missing required role", func(t *testing.T) {
		v := oidcTokenVerifier(fakeVerifier{claims: &grpcauth.Claims{Subject: "user-3", Roles: []string{"viewer"}}}, "uploader")
		_, err := v(ctx, "tok", nil)
		if !errors.Is(err, sdkauth.ErrInvalidToken) {
			t.Fatalf("err = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("verifier error", func(t *testing.T) {
		v := oidcTokenVerifier(fakeVerifier{err: errors.New("expired")}, "")
		_, err := v(ctx, "tok", nil)
		if !errors.Is(err, sdkauth.ErrInvalidToken) {
			t.Fatalf("err = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("nil claims without error", func(t *testing.T) {
		v := oidcTokenVerifier(fakeVerifier{}, "")
		_, err := v(ctx, "tok", nil)
		if !errors.Is(err, sdkauth.ErrInvalidToken) {
			t.Fatalf("err = %v, want ErrInvalidToken", err)
		}
	})
}

type fakePresigner struct {
	key  string
	opts uploads.PutOptions
}

func (f *fakePresigner) PresignPut(_ context.Context, key string, o uploads.PutOptions, _ time.Duration) (string, error) {
	f.key, f.opts = key, o
	return "https://s3.example/" + key + "?sig=x", nil
}

// connectTestClient registers tools on a fresh server and connects an in-memory client.
func connectTestClient(t *testing.T, svc *uploads.Service) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	registerTools(srv, svc)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// decodeStructured round-trips StructuredContent through JSON into out.
func decodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func TestRegisterToolsListAllowedTypes(t *testing.T) {
	f := &fakePresigner{}
	svc, err := uploads.New(uploads.Config{
		MaxBytes: 10 << 20, PublicBaseURL: "https://media.example/", PresignTTL: time.Minute,
	}, f)
	if err != nil {
		t.Fatal(err)
	}
	cs := connectTestClient(t, svc)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_allowed_types", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	var out listTypesOut
	decodeStructured(t, res, &out)
	if out.MaxBytes != 10<<20 {
		t.Errorf("max_bytes = %d, want %d", out.MaxBytes, 10<<20)
	}
	if out.Extensions[".gif"] != "image/gif" {
		t.Errorf("extensions[.gif] = %q", out.Extensions[".gif"])
	}
	if len(out.Extensions) != len(uploads.AllowedExtensions()) {
		t.Errorf("got %d extensions, want %d", len(out.Extensions), len(uploads.AllowedExtensions()))
	}
}

func TestRegisterToolsRequestUpload(t *testing.T) {
	ctx := context.Background()
	f := &fakePresigner{}
	svc, err := uploads.New(uploads.Config{
		MaxBytes: 10 << 20, PublicBaseURL: "https://media.example/",
		Now:   func() time.Time { return time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC) },
		NewID: func() string { return "abc123" },
	}, f)
	if err != nil {
		t.Fatal(err)
	}
	cs := connectTestClient(t, svc)

	t.Run("permanent upload", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "request_upload",
			Arguments: map[string]any{"filename": "demo.gif", "size_bytes": 1024},
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatalf("tool error: %+v", res.Content)
		}
		var out uploads.Result
		decodeStructured(t, res, &out)
		wantKey := "permanent/2026/11/abc123/demo.gif"
		if out.Key != wantKey {
			t.Errorf("key = %q, want %q", out.Key, wantKey)
		}
		if out.PublicURL != "https://media.example/"+wantKey {
			t.Errorf("public_url = %q", out.PublicURL)
		}
		if !strings.Contains(out.UploadURL, wantKey) {
			t.Errorf("upload_url = %q", out.UploadURL)
		}
		if f.key != wantKey || f.opts.ContentType != "image/gif" || f.opts.ContentLength != 1024 {
			t.Errorf("presigner got key=%q opts=%+v", f.key, f.opts)
		}
	})

	t.Run("temporary upload uses tmp prefix", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "request_upload",
			Arguments: map[string]any{"filename": "clip.mp4", "size_bytes": 5, "temporary": true},
		})
		if err != nil {
			t.Fatal(err)
		}
		var out uploads.Result
		decodeStructured(t, res, &out)
		if !strings.HasPrefix(out.Key, "tmp/") {
			t.Errorf("key = %q, want tmp/ prefix", out.Key)
		}
	})

	t.Run("disallowed extension is a tool error", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name:      "request_upload",
			Arguments: map[string]any{"filename": "x.exe", "size_bytes": 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatal("IsError = false, want true")
		}
	})
}
