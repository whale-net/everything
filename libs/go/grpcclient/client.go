package grpcclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Client manages gRPC connections
type Client struct {
	conn *grpc.ClientConn
}

// TLSConfig holds TLS/SSL configuration for gRPC connections
type TLSConfig struct {
	// Enabled determines if TLS should be used
	Enabled bool
	// InsecureSkipVerify controls whether to verify the server's certificate chain and host name
	// Setting this to true is insecure and should only be used for development/testing
	InsecureSkipVerify bool
	// CACertPath is the path to a custom CA certificate file for verifying the server certificate
	CACertPath string
	// ServerName is used to verify the hostname on the server certificate
	// Use this when the connection URL hostname differs from the certificate hostname
	// Example: connecting to internal k8s service but cert is for external domain
	ServerName string
}

// NewClient creates a new gRPC client connected to the given address
// address can be either:
// - TCP: "host:port" (e.g., "localhost:50051")
// - Unix socket: "unix:///path/to/socket"
//
// TLS configuration is loaded from environment variables with GRPC_ prefix:
//   - GRPC_USE_TLS=true (optional): Force TLS (auto-detects if address contains :443 or https://)
//   - GRPC_TLS_SKIP_VERIFY=false (optional): Disable certificate verification (insecure, dev only)
//   - GRPC_CA_CERT_PATH=/path/to/ca.crt (optional): Custom CA certificate
//   - GRPC_TLS_SERVER_NAME=api.example.com (optional): Server name for certificate verification
func NewClient(ctx context.Context, address string) (*Client, error) {
	// Load TLS config from environment
	var tlsConfig *TLSConfig

	// Check if TLS should be used (explicit or auto-detect)
	useTLS := shouldUseTLS(address)
	if envUseTLS := os.Getenv("GRPC_USE_TLS"); envUseTLS != "" {
		useTLS = envUseTLS == "true"
	}

	if useTLS {
		tlsConfig = getTLSConfigFromEnv("GRPC_")
		tlsConfig.Enabled = true
	}

	return NewClientWithTLS(ctx, address, tlsConfig)
}

// NewClientWithTLS creates a new gRPC client with explicit TLS configuration
func NewClientWithTLS(ctx context.Context, address string, tlsConfig *TLSConfig) (*Client, error) {
	var opts []grpc.DialOption

	// Determine connection type
	if len(address) > 7 && address[:7] == "unix://" {
		// Unix socket connection
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		opts = append(opts, grpc.WithContextDialer(unixDialer))
		address = address[7:] // Remove "unix://" prefix
	} else {
		// TCP connection - check for TLS
		if tlsConfig != nil && tlsConfig.Enabled {
			creds, err := buildTLSCredentials(tlsConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to build TLS credentials: %w", err)
			}
			opts = append(opts, grpc.WithTransportCredentials(creds))
		} else {
			opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		}
	}

	opts = append(opts, grpc.WithBlock())

	conn, err := grpc.DialContext(ctx, address, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial gRPC server: %w", err)
	}

	return &Client{conn: conn}, nil
}

// Close closes the gRPC connection
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// GetConnection returns the underlying gRPC connection
func (c *Client) GetConnection() *grpc.ClientConn {
	return c.conn
}

// unixDialer is a dialer function for Unix sockets
func unixDialer(ctx context.Context, addr string) (net.Conn, error) {
	return net.Dial("unix", addr)
}

// getTLSConfigFromEnv creates TLS configuration from environment variables with given prefix
func getTLSConfigFromEnv(prefix string) *TLSConfig {
	config := &TLSConfig{
		Enabled:            false,
		InsecureSkipVerify: false,
	}

	// Check if TLS verification should be disabled
	if verify := os.Getenv(prefix + "TLS_SKIP_VERIFY"); verify == "true" {
		config.InsecureSkipVerify = true
	}

	// Check for custom CA certificate path
	if caPath := os.Getenv(prefix + "CA_CERT_PATH"); caPath != "" {
		config.CACertPath = caPath
	}

	// Check for custom server name for certificate verification
	if serverName := os.Getenv(prefix + "TLS_SERVER_NAME"); serverName != "" {
		config.ServerName = serverName
	}

	return config
}

// buildTLSCredentials creates gRPC transport credentials from TLSConfig
func buildTLSCredentials(config *TLSConfig) (credentials.TransportCredentials, error) {
	if config == nil {
		return insecure.NewCredentials(), nil
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: config.InsecureSkipVerify,
	}

	// Set server name for certificate verification if provided
	if config.ServerName != "" {
		tlsConfig.ServerName = config.ServerName
	}

	// Load custom CA certificate if provided
	if config.CACertPath != "" {
		caCert, err := os.ReadFile(config.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate from %s: %w", config.CACertPath, err)
		}

		caCertPool := x509.NewCertPool()
		if ok := caCertPool.AppendCertsFromPEM(caCert); !ok {
			return nil, fmt.Errorf("failed to parse CA certificate from %s", config.CACertPath)
		}

		tlsConfig.RootCAs = caCertPool
	}

	return credentials.NewTLS(tlsConfig), nil
}

// shouldUseTLS determines if TLS should be used based on the address
// Returns true if address contains :443 or starts with https://
func shouldUseTLS(address string) bool {
	lower := strings.ToLower(address)
	return strings.HasPrefix(lower, "https://") || strings.Contains(lower, ":443")
}
