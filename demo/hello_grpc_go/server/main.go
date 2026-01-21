package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	pb "github.com/whale-net/everything/demo/hello_grpc_go/protos"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// server implements the Greeter service
type server struct {
	pb.UnimplementedGreeterServer
}

// SayHello implements the SayHello RPC
func (s *server) SayHello(ctx context.Context, req *pb.HelloRequest) (*pb.HelloResponse, error) {
	log.Printf("Received: %s", req.GetName())
	return &pb.HelloResponse{
		Message: fmt.Sprintf("Hello, %s!", req.GetName()),
		Count:   1,
	}, nil
}

// SayHelloStream implements the SayHelloStream RPC (server-side streaming)
func (s *server) SayHelloStream(req *pb.HelloRequest, stream pb.Greeter_SayHelloStreamServer) error {
	log.Printf("Received streaming request: %s", req.GetName())

	for i := 1; i <= 5; i++ {
		if err := stream.Send(&pb.HelloResponse{
			Message: fmt.Sprintf("Hello #%d, %s!", i, req.GetName()),
			Count:   int32(i),
		}); err != nil {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

func main() {
	port := 50051
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterGreeterServer(s, &server{})

	// Register reflection service for debugging with grpcurl
	reflection.Register(s)

	log.Printf("Server listening on :%d", port)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
