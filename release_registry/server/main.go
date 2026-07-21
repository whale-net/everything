// Package main implements a stub gRPC registry-server for the release registry.
package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	pb "github.com/whale-net/everything/release_registry/proto/gen"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const defaultPort = 50054

// server is a minimal gRPC server that accepts all RPCs without implementing them.
type server struct {
	pb.UnimplementedRegistryServiceServer
}

func main() {
	port := defaultPort
	if p := os.Getenv("PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			port = n
		}
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatalf("failed to listen on port %d: %v", port, err)
	}

	srv := grpc.NewServer(reflection.ServerOption())
	pb.RegisterRegistryServiceServer(srv, &server{})

	go func() {
		log.Printf("registry-server listening on :%d (reflection enabled)", port)
		if err := srv.Serve(lis); err != nil {
			log.Fatalf("gRPC serve error: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down registry-server...")
	srv.GracefulStop()
}
