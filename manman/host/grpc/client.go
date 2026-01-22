package grpc

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client manages gRPC connections to wrapper containers
type Client struct {
	conn *grpc.ClientConn
}

// NewClient creates a new gRPC client connected to the given address
// address can be either:
// - TCP: "host:port" (e.g., "localhost:50051")
// - Unix socket: "unix:///path/to/socket"
func NewClient(ctx context.Context, address string) (*Client, error) {
	var opts []grpc.DialOption

	// Determine connection type
	if len(address) > 7 && address[:7] == "unix://" {
		// Unix socket connection
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		opts = append(opts, grpc.WithContextDialer(unixDialer))
		address = address[7:] // Remove "unix://" prefix
	} else {
		// TCP connection
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
