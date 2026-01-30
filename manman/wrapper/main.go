package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/whale-net/everything/libs/go/docker"
	pb "github.com/whale-net/everything/manman/protos"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Fatal error: %v", err)
	}
}

func run() error {
	// Get configuration from environment
	port := getEnv("GRPC_PORT", "50051")
	dockerSocket := getEnv("DOCKER_SOCKET", "/var/run/docker.sock")
	sessionID := getEnv("SESSION_ID", "")
	sgcID := getEnv("SGC_ID", "")
	serverID := getEnv("SERVER_ID", "")
	networkName := getEnv("NETWORK_NAME", "")

	log.Printf("Starting ManManV2 Wrapper Service")
	log.Printf("gRPC port: %s", port)
	log.Printf("Session ID: %s", sessionID)
	log.Printf("SGC ID: %s", sgcID)
	log.Printf("Server ID: %s", serverID)
	log.Printf("Network: %s", networkName)

	// Initialize Docker client
	log.Println("Connecting to Docker...")
	dockerClient, err := docker.NewClient(dockerSocket)
	if err != nil {
		return fmt.Errorf("failed to initialize Docker client: %w", err)
	}
	defer dockerClient.Close()

	// Try to load previous state (for recovery after restart)
	log.Println("Checking for previous state...")
	previousState, err := LoadState()
	if err != nil {
		log.Printf("Warning: Failed to load previous state: %v", err)
	} else if previousState != nil {
		log.Printf("Recovered previous state: session_id=%d, status=%s, container=%s",
			previousState.SessionID, previousState.Status, previousState.GameContainerID)
	}

	// Create gRPC server
	grpcServer := grpc.NewServer()

	// Register WrapperControl service
	wrapperServer := newServer(dockerClient, sessionID, sgcID, serverID, networkName, previousState)
	pb.RegisterWrapperControlServer(grpcServer, wrapperServer)
	
	// Register reflection service for debugging with grpcurl
	reflection.Register(grpcServer)
	
	log.Printf("Registered WrapperControl service")

	// Start listening
	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %w", port, err)
	}

	// Channel for server errors
	serverErrors := make(chan error, 1)

	// Start gRPC server in goroutine
	go func() {
		log.Printf("Wrapper gRPC server listening on :%s", port)
		if err := grpcServer.Serve(lis); err != nil {
			serverErrors <- fmt.Errorf("gRPC server error: %w", err)
		}
	}()

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Wait for shutdown signal or server error
	select {
	case err := <-serverErrors:
		return err
	case sig := <-sigChan:
		log.Printf("Received signal %v, initiating graceful shutdown", sig)
		
		// Graceful shutdown with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		
		// Stop accepting new requests and wait for existing to complete
		stopped := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(stopped)
		}()
		
		select {
		case <-stopped:
			log.Println("Graceful shutdown completed")
		case <-ctx.Done():
			log.Println("Graceful shutdown timeout, forcing stop")
			grpcServer.Stop()
		}
	}

	return nil
}

// getEnv retrieves an environment variable or returns a default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
