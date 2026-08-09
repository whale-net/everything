package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// idempotencyKeyFlag backs --idempotency-key on write commands. A client- or
// CI-generated value; see ARCHITECTURE.md "Idempotency" for the convention.
var idempotencyKeyFlag string

// registryClient wraps a connection and the four App Registry service
// clients. One dial per invocation — this is a CLI, not a long-lived process.
type registryClient struct {
	conn        *grpcclient.Client
	App         pb.AppRegistryClient
	Artifact    pb.ArtifactRegistryClient
	Promotion   pb.PromotionRegistryClient
	Environment pb.EnvironmentRegistryClient
}

func newRegistryClient(ctx context.Context) (*registryClient, error) {
	authOpt, err := grpcauth.NewServiceAccountDialOption(grpcauth.ClientConfig{
		Mode:         grpcauth.AuthMode(getEnv("GRPC_AUTH_MODE", "none")),
		TokenURL:     os.Getenv("GRPC_AUTH_TOKEN_URL"),
		ClientID:     os.Getenv("GRPC_AUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("GRPC_AUTH_CLIENT_SECRET"),
		// RequireTransportSecurity left false: TLS is handled independently
		// by grpcclient.NewClient via GRPC_USE_TLS (see ENV.md); this only
		// controls whether PerRPCCredentials refuses to send the bearer
		// token over a plaintext transport, same as manmanv2/host and
		// manmanv2/log-processor.
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create auth dial option: %w", err)
	}

	conn, err := grpcclient.NewClient(ctx, serverAddr, authOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to app-registry-api at %s: %w", serverAddr, err)
	}
	c := conn.GetConnection()
	return &registryClient{
		conn:        conn,
		App:         pb.NewAppRegistryClient(c),
		Artifact:    pb.NewArtifactRegistryClient(c),
		Promotion:   pb.NewPromotionRegistryClient(c),
		Environment: pb.NewEnvironmentRegistryClient(c),
	}, nil
}

func (c *registryClient) Close() error {
	return c.conn.Close()
}

// getEnv reads an environment variable, falling back to defaultValue when
// unset or empty — matches the convention manmanv2/host and
// manmanv2/log-processor use for their auth env vars.
func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// printResponse formats a proto response per the --format flag. table is not
// implemented yet in AR-1 — it falls back to JSON with a note. Formatting
// choices live here, not domain logic, so this stays on the CLI side.
func printResponse(msg proto.Message) error {
	b, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to format response: %w", err)
	}
	if format == "table" {
		fmt.Println("# table format not implemented yet, showing json")
	}
	fmt.Println(string(b))
	return nil
}

// withClient dials the server, runs fn, and always closes the connection —
// the shared boilerplate every leaf command needs around one RPC call.
func withClient(cmd *cobra.Command, fn func(rc *registryClient) error) error {
	rc, err := newRegistryClient(cmd.Context())
	if err != nil {
		return err
	}
	defer rc.Close() //nolint:errcheck
	return fn(rc)
}

// unmarshalJSON decodes JSON into a proto message using the manifest
// contract's strict mode (DiscardUnknown: false) so a stale CLI build fails
// loudly instead of silently dropping fields. See appmeta.proto.
func unmarshalJSON(data []byte, msg proto.Message) error {
	return protojson.UnmarshalOptions{DiscardUnknown: false}.Unmarshal(data, msg)
}
