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
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/manmanv2/mcp/server"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

func main() {
	logging.Configure(logging.Config{ServiceName: "manmanv2-mcp", Domain: "manmanv2", AppType: "external-api", JSONFormat: true})
	logger := logging.Get("manmanv2/mcp")
	if err := run(logger); err != nil {
		logger.Error("manmanv2 mcp exited", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	issuer, clientID := os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID")
	if issuer == "" || clientID == "" {
		return errors.New("OIDC_ISSUER and OIDC_CLIENT_ID are required: the MCP server has no unauthenticated mode")
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

	reg := server.NewRegistry(append(append([]server.Tool{server.WhoamiTool, server.ConnectAddressTool}, server.ReadTools...), server.WorkshopTools...)...)
	srv := server.NewServer(reg, server.LogAuditor{Logger: logging.Get("manmanv2/mcp/audit")}, server.SQLIdempotencyStore{DB: db})
	apiClient := manmanpb.NewManManAPIClient(conn.GetConnection())
	server.AddReadTools(srv, apiClient)
	server.AddConnectAddressTool(srv, apiClient)
	server.AddWorkshopTools(srv, manmanpb.NewWorkshopServiceClient(conn.GetConnection()), &server.Gate{Store: server.SQLConfirmationStore{DB: db}})

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/", server.HTTPAuth(verifier, os.Getenv("MCP_RESOURCE_METADATA_URL"))(mcpHandler))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	httpSrv := &http.Server{Addr: ":" + port, Handler: otelhttp.NewHandler(mux, "manmanv2-mcp"), ReadHeaderTimeout: 10 * time.Second}
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
