package grpc

import (
	"context"
	"fmt"
	"io"

	"github.com/whale-net/everything/libs/go/grpcclient"
	pb "github.com/whale-net/everything/manman/protos"
)

// WrapperControlClient wraps the gRPC WrapperControl service client
type WrapperControlClient struct {
	client pb.WrapperControlClient
}

// NewWrapperControlClient creates a new wrapper control client
func NewWrapperControlClient(grpcClient *grpcclient.Client) *WrapperControlClient {
	return &WrapperControlClient{
		client: pb.NewWrapperControlClient(grpcClient.GetConnection()),
	}
}

// Start starts a game server container
func (c *WrapperControlClient) Start(ctx context.Context, req *pb.StartRequest) (*pb.StartResponse, error) {
	return c.client.Start(ctx, req)
}

// Stop stops a game server container
func (c *WrapperControlClient) Stop(ctx context.Context, req *pb.StopRequest) (*pb.StopResponse, error) {
	return c.client.Stop(ctx, req)
}

// SendInput sends input to the game server process
func (c *WrapperControlClient) SendInput(ctx context.Context, req *pb.SendInputRequest) (*pb.SendInputResponse, error) {
	return c.client.SendInput(ctx, req)
}

// GetStatus returns the current status of the wrapper and game server
func (c *WrapperControlClient) GetStatus(ctx context.Context, req *pb.GetStatusRequest) (*pb.GetStatusResponse, error) {
	return c.client.GetStatus(ctx, req)
}

// StreamOutput streams stdout/stderr from the game server
func (c *WrapperControlClient) StreamOutput(ctx context.Context, req *pb.StreamOutputRequest) (WrapperOutputStream, error) {
	stream, err := c.client.StreamOutput(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create output stream: %w", err)
	}
	return &wrapperOutputStream{stream: stream}, nil
}

// WrapperOutputStream is an interface for streaming output
type WrapperOutputStream interface {
	Recv() (*pb.StreamOutputResponse, error)
	CloseSend() error
}

type wrapperOutputStream struct {
	stream pb.WrapperControl_StreamOutputClient
}

func (s *wrapperOutputStream) Recv() (*pb.StreamOutputResponse, error) {
	return s.stream.Recv()
}

func (s *wrapperOutputStream) CloseSend() error {
	return s.stream.CloseSend()
}

// ReadAll reads all output from the stream until EOF
func ReadAll(stream WrapperOutputStream) ([]byte, []byte, error) {
	var stdout, stderr []byte
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return stdout, stderr, fmt.Errorf("failed to receive output: %w", err)
		}
		if resp.Eof {
			break
		}
		if resp.IsStderr {
			stderr = append(stderr, resp.Data...)
		} else {
			stdout = append(stdout, resp.Data...)
		}
	}
	return stdout, stderr, nil
}
