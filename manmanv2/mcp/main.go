// Command mcp is the manmanv2 MCP server: authenticated, persona-gated tools
// over the manmanv2 control API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	reg := server.NewRegistry(server.WhoamiTool, server.ConnectAddressTool)
	srv := server.NewServer(reg, server.LogAuditor{Logger: logging.Get("manmanv2/mcp/audit")})

	controlAddr := os.Getenv("CONTROL_API_URL")
	if controlAddr == "" {
		controlAddr = "control-api-dev-service:50051"
	}
	// The user-token dial option forwards the caller's token on every backend call.
	conn, err := grpcclient.NewClient(ctx, controlAddr, grpcauth.NewUserTokenDialOption(grpcauth.AuthMode(os.Getenv("GRPC_AUTH_MODE"))))
	if err != nil {
		return fmt.Errorf("control api client: %w", err)
	}
	defer conn.Close()
	server.AddConnectAddressTool(srv, manmanpb.NewManManAPIClient(conn.GetConnection()))

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
