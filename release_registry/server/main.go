// Package main implements a stub gRPC registry-server for the release registry.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	pb "github.com/whale-net/everything/release_registry/proto/gen"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/release_registry/internal/auth"
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

	ctx := context.Background()

	var opts []grpc.ServerOption
	if mode := grpcauth.AuthMode(os.Getenv("GRPC_AUTH_MODE")); mode == "oidc" {
		log.Println("registry-server: OIDC auth enabled (Keycloak)")
	} else {
		log.Println("registry-server: auth disabled — dev mode (no Keycloak required)")
	}

	unaryInt, streamInt, err := auth.NewServerInterceptors(ctx)
	if err != nil {
		log.Fatalf("failed to create auth interceptors: %v", err)
	}

	opts = append(opts, grpc.StreamInterceptor(streamInt))
	opts = append(opts, grpc.UnaryInterceptor(unaryInt))
	opts = append(opts, reflection.ServerOption())

	srv := grpc.NewServer(opts...)
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
