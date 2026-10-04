// Command mcp is the manmanv2 MCP server: authenticated, persona-gated tools
// over the manmanv2 control API.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantflow"
	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/whagent"
	"github.com/whale-net/everything/manmanv2/identitylink"
	"github.com/whale-net/everything/manmanv2/mcp/admin"
	"github.com/whale-net/everything/manmanv2/mcp/server"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

func main() {
	logging.Configure(logging.Config{
		ServiceName:   "manmanv2-mcp",
		Domain:        "manmanv2",
		AppType:       "external-api",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	logger := logging.Get("manmanv2/mcp")
	err := run(logger)
	if err != nil {
		logger.Error("manmanv2 mcp exited", "error", err)
	}
	// Flush buffered spans and logs before exit; os.Exit skips defers.
	_ = logging.Shutdown(context.Background())
	if err != nil {
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	issuer, clientID := os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID")
	if issuer == "" || clientID == "" {
		return errors.New("OIDC_ISSUER and OIDC_CLIENT_ID are required: the MCP server has no unauthenticated mode")
	}
	_, grantSet := grantflow.ConfigFromEnv(os.Getenv, issuer)
	whagentEnv, err := server.WhagentEnvFromEnv(os.Getenv, grantSet)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	verifier, err := grpcauth.NewOIDCVerifier(ctx, issuer, clientID)
	if err != nil {
		return fmt.Errorf("oidc verifier: %w", err)
	}

	dbURL := os.Getenv("PG_DATABASE_URL")
	if dbURL == "" {
		return errors.New("PG_DATABASE_URL is required: write-tool idempotency records are persisted")
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	apiAddr := os.Getenv("CONTROL_API_URL")
	if apiAddr == "" {
		return errors.New("CONTROL_API_URL is required")
	}
	conn, err := grpcclient.NewClient(ctx, apiAddr, grpcauth.NewUserTokenDialOption(grpcauth.AuthModeOIDC))
	if err != nil {
		return fmt.Errorf("control api: %w", err)
	}
	defer conn.Close()

	apiClient := manmanpb.NewManManAPIClient(conn.GetConnection())
	tools := append([]server.Tool{server.WhoamiTool, server.ConnectAddressTool}, server.ReadTools...)
	tools = append(tools, server.LifecycleTools...)
	tools = append(tools, server.SessionActionTools...)
	tools = append(tools, server.EditTools(apiClient)...)
	tools = append(tools, admin.Tools(apiClient)...)
	tools = append(tools, server.WorkshopTools...)
	tools = append(tools, server.ActionDefinitionTools(apiClient)...)
	reg := server.NewRegistry(tools...)
	srv := server.NewServer(reg, server.LogAuditor{Logger: logging.Get("manmanv2/mcp/audit")}, server.SQLIdempotencyStore{DB: db})
	server.AddReadTools(srv, apiClient)
	server.AddConnectAddressTool(srv, apiClient)
	server.AddLifecycleTools(srv, apiClient, server.SQLStartAllowlist{DB: db}, &server.Gate{Store: server.SQLConfirmationStore{DB: db}})
	server.AddSessionActionTools(srv, apiClient, server.SQLActionAllowlist{DB: db})
	server.AddEditTools(srv, &server.Gate{Store: server.SQLConfirmationStore{DB: db}}, apiClient)
	admin.Register(srv, apiClient, &server.Gate{Store: server.SQLConfirmationStore{DB: db}})
	server.AddActionDefinitionTools(srv, apiClient, &server.Gate{Store: server.SQLConfirmationStore{DB: db}})
	server.AddWorkshopTools(srv, manmanpb.NewWorkshopServiceClient(conn.GetConnection()), &server.Gate{Store: server.SQLConfirmationStore{DB: db}})

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	verify, authServer, ex, err := callerVerifier(ctx, logger, dbURL, verifier, issuer)
	if err != nil {
		return err
	}
	var wrap func(string) func(http.Handler) http.Handler
	if whagentEnv.Enabled() {
		wv, err := whagent.NewVerifier(ctx, whagentEnv.JWKSURL, whagentEnv.Issuer)
		if err != nil {
			return fmt.Errorf("whagent verifier: %w", err)
		}
		cfg := server.WhagentAuthConfig{Verifier: wv, Audience: os.Getenv("MCP_PUBLIC_URL"), UserIssuer: issuer, WhagentIssuer: whagentEnv.Issuer}
		wrap = func(metaURL string) func(http.Handler) http.Handler {
			return server.WhagentHTTPAuth(verify, cfg, metaURL)
		}
		// Added last, so it runs outermost: the Caller exists before persona gating.
		srv.AddReceivingMiddleware(server.WhagentMiddleware(*ex, identitylink.Store{DB: db}, server.LogAuditor{Logger: logging.Get("manmanv2/mcp/audit")}))
		logger.Info("whagent-net credentials accepted", "issuer", whagentEnv.Issuer)
	}
	handler := server.NewHandler(mcpHandler, verify, authServer, os.Getenv("MCP_PUBLIC_URL"), os.Getenv("MCP_RESOURCE_METADATA_URL"), wrap)
	if os.Getenv("MCP_PUBLIC_URL") == "" {
		logger.Warn("MCP_PUBLIC_URL unset: no protected-resource metadata served, clients cannot discover OAuth")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	httpSrv := &http.Server{Addr: ":" + port, Handler: otelhttp.NewHandler(handler, "manmanv2-mcp"), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	logger.Info("manmanv2 mcp listening", "addr", httpSrv.Addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// callerVerifier picks how callers authenticate. With the GRANT_* variables
// set the MCP behaves like krill/ASS/whagent-net: clients hold opaque
// credentials issued by the UI's authorization server (UI_PUBLIC_URL) and
// the stored per-user grant yields the Keycloak token forwarded to the
// control API. Without them it accepts Keycloak tokens directly.
func callerVerifier(ctx context.Context, logger *slog.Logger, dbURL string, oidc grpcauth.TokenVerifier, issuer string) (server.CallerVerifier, string, *grantflow.Exchanger, error) {
	grantCfg, ok := grantflow.ConfigFromEnv(os.Getenv, issuer)
	if !ok {
		logger.Warn("GRANT_* unset: accepting Keycloak tokens directly; clients need --client-id")
		return server.OIDCCallerVerifier(oidc), issuer, nil, nil
	}
	uiURL := strings.TrimRight(os.Getenv("UI_PUBLIC_URL"), "/")
	if uiURL == "" {
		return nil, "", nil, errors.New("UI_PUBLIC_URL is required with GRANT_*: it hosts the OAuth authorization server")
	}
	pool, err := db.NewPool(ctx, dbURL)
	if err != nil {
		return nil, "", nil, fmt.Errorf("grant database: %w", err)
	}
	creds, err := auth.NewCredentialStore(ctx, auth.StoreConfig{Pool: pool})
	if err != nil {
		return nil, "", nil, fmt.Errorf("mcp credential store: %w", err)
	}
	grants, err := grantflow.Build(ctx, grantCfg, pool)
	if err != nil {
		return nil, "", nil, err
	}
	ex := grantflow.Exchanger{Source: grants.Source, Grant: grantflow.DefaultGrant, Verifier: oidc}
	return server.CredentialCallerVerifier(creds, ex), uiURL, &ex, nil
}
