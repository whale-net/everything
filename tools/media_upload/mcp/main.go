// Command mcp is the media-upload MCP server: it issues size-capped S3
// presigned PUT URLs and the matching public URL for embeddable media.
// Callers authenticate with Keycloak OAuth access tokens.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/mcpobs"
	s3lib "github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/tools/media_upload/uploads"
)

const serviceName = "media-upload-mcp"

func main() {
	logging.Configure(logging.Config{
		ServiceName:   serviceName,
		Domain:        "media-upload",
		Level:         slog.LevelInfo,
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	logger := logging.Get("main")
	err := run(logger)
	if err != nil {
		logger.Error("media-upload mcp exited", "error", err)
	}
	// os.Exit skips defers, so flush explicitly.
	_ = logging.Shutdown(context.Background())
	if err != nil {
		os.Exit(1)
	}
}

type s3Presigner struct{ c *s3lib.Client }

func (p s3Presigner) PresignPut(ctx context.Context, key string, o uploads.PutOptions, ttl time.Duration) (string, error) {
	return p.c.PresignPublicPutObject(ctx, key, s3lib.PresignPutOptions{
		ContentType: o.ContentType, ContentLength: o.ContentLength, CacheControl: o.CacheControl,
	}, ttl)
}

func run(logger *slog.Logger) error {
	issuer, clientID := os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID")
	publicURL := strings.TrimRight(os.Getenv("MCP_PUBLIC_URL"), "/")
	bucket, mediaBase := os.Getenv("S3_BUCKET"), os.Getenv("MEDIA_PUBLIC_BASE_URL")
	for k, v := range map[string]string{
		"OIDC_ISSUER": issuer, "OIDC_CLIENT_ID": clientID, "MCP_PUBLIC_URL": publicURL,
		"S3_BUCKET": bucket, "S3_PUBLIC_ENDPOINT": os.Getenv("S3_PUBLIC_ENDPOINT"), "MEDIA_PUBLIC_BASE_URL": mediaBase,
	} {
		if v == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	maxMB, err := envInt("MEDIA_MAX_MB", 50)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	verifier, err := grpcauth.NewOIDCVerifier(ctx, issuer, clientID)
	if err != nil {
		return fmt.Errorf("oidc verifier: %w", err)
	}
	s3c, err := s3lib.NewClient(ctx, s3lib.Config{
		Bucket:         bucket,
		Region:         envOr("S3_REGION", "us-east-1"),
		Endpoint:       os.Getenv("S3_ENDPOINT"),
		PublicEndpoint: os.Getenv("S3_PUBLIC_ENDPOINT"),
		AccessKey:      os.Getenv("S3_ACCESS_KEY"),
		SecretKey:      os.Getenv("S3_SECRET_KEY"),
		// Local MinIO routes by path only.
		PublicUsePathStyle: os.Getenv("S3_PUBLIC_PATH_STYLE") == "true",
	})
	if err != nil {
		return fmt.Errorf("s3 client: %w", err)
	}
	svc, err := uploads.New(uploads.Config{
		MaxBytes:        int64(maxMB) << 20,
		PublicBaseURL:   mediaBase,
		TempPrefix:      envOr("MEDIA_TEMP_PREFIX", "tmp"),
		PermanentPrefix: envOr("MEDIA_PERMANENT_PREFIX", "permanent"),
	}, s3Presigner{s3c})
	if err != nil {
		return err
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: serviceName, Version: "v0"}, nil)
	registerTools(srv, svc)
	srv.AddReceivingMiddleware(mcpobs.ToolCallMiddleware(logging.Tracer(serviceName), logging.Get("tools"), callerAttr))

	meta := auth.ProtectedResourceMetadataConfig{Resource: publicURL, AuthorizationServer: issuer, ResourceName: "Media Upload MCP"}
	opts := auth.ResourceServerBearerOptions(meta)
	opts.AllowMissingExpiration = true
	guard := sdkauth.RequireBearerToken(oidcTokenVerifier(verifier, os.Getenv("MEDIA_REQUIRED_ROLE")), opts)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	handler := auth.NewResourceServerMux(meta, map[string]http.Handler{"/": guard(mcpHandler)})

	httpSrv := &http.Server{
		Addr:              ":" + envOr("PORT", "8081"),
		Handler:           mcpobs.NewHTTPHandler(handler, serviceName),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(sctx)
	}()
	logger.Info("listening", "addr", httpSrv.Addr, "max_mb", maxMB)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

const subjectKey = "subject"

// oidcTokenVerifier accepts Keycloak tokens, optionally requiring a realm role.
func oidcTokenVerifier(v grpcauth.TokenVerifier, requiredRole string) sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		claims, err := v.Verify(ctx, token)
		if err != nil || claims == nil {
			return nil, sdkauth.ErrInvalidToken
		}
		if requiredRole != "" && !hasRole(claims.Roles, requiredRole) {
			return nil, sdkauth.ErrInvalidToken
		}
		return &sdkauth.TokenInfo{UserID: claims.Subject, Extra: map[string]any{subjectKey: claims.Subject}}, nil
	}
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func callerAttr(req mcp.Request) (string, string, bool) {
	if extra := req.GetExtra(); extra != nil && extra.TokenInfo != nil && extra.TokenInfo.UserID != "" {
		return "subject", extra.TokenInfo.UserID, true
	}
	return "", "", false
}

type requestUploadIn struct {
	Filename    string `json:"filename" jsonschema:"file name including extension, e.g. demo.gif"`
	SizeBytes   int64  `json:"size_bytes" jsonschema:"exact size in bytes of the file you will upload"`
	ContentType string `json:"content_type,omitempty" jsonschema:"optional MIME type; must match the extension"`
	Temporary   bool   `json:"temporary,omitempty" jsonschema:"true = stored under the expiring prefix; default false (permanent)"`
}

type listTypesOut struct {
	MaxBytes   int64             `json:"max_bytes"`
	Extensions map[string]string `json:"extensions"`
}

func registerTools(srv *mcp.Server, svc *uploads.Service) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "request_upload",
		Description: "Get a presigned PUT URL and the final public URL for a media file. " +
			"Upload with: PUT <upload_url> sending exactly the returned headers and a body of size_bytes bytes. " +
			"Then embed public_url (e.g. in a GitHub PR description).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in requestUploadIn) (*mcp.CallToolResult, *uploads.Result, error) {
		res, err := svc.Presign(ctx, uploads.Request{
			Filename: in.Filename, ContentType: in.ContentType, SizeBytes: in.SizeBytes, Temporary: in.Temporary,
		})
		if err != nil {
			return nil, nil, err
		}
		return nil, res, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_allowed_types",
		Description: "List the allowed file extensions (with MIME types) and the max upload size.",
	}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, listTypesOut, error) {
		return nil, listTypesOut{MaxBytes: svc.MaxBytes(), Extensions: uploads.AllowedExtensions()}, nil
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", k, v)
	}
	return n, nil
}
