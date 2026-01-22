package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"
)

// Client wraps the Docker client with additional functionality
type Client struct {
	cli *client.Client
}

// NewClient creates a new Docker client from the socket path
func NewClient(socketPath string) (*Client, error) {
	if socketPath == "" {
		socketPath = "/var/run/docker.sock"
	}

	cli, err := client.NewClientWithOpts(
		client.WithHost(fmt.Sprintf("unix://%s", socketPath)),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	// Test connection
	ctx := context.Background()
	_, err = cli.Ping(ctx)
	if err != nil {
		// Close the client on ping failure to prevent resource leak
		cli.Close()
		return nil, fmt.Errorf("failed to ping Docker daemon: %w", err)
	}

	return &Client{cli: cli}, nil
}

// Close closes the Docker client connection
func (c *Client) Close() error {
	return c.cli.Close()
}

// GetClient returns the underlying Docker client (for advanced operations)
func (c *Client) GetClient() *client.Client {
	return c.cli
}
