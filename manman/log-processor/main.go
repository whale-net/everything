package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manman/log-processor/consumer"
	"github.com/whale-net/everything/manman/log-processor/server"
	manmanpb "github.com/whale-net/everything/manman/protos"
)

func main() {
	log.Println("Starting log-processor service...")

	// Load configuration
	config := LoadConfig()
	log.Printf("Configuration: RabbitMQ=%s, gRPC Port=%s, Buffer TTL=%ds, Max Messages=%d",
		config.RabbitMQURL, config.GRPCPort, config.LogBufferTTL, config.LogBufferMaxMsgs)

	// Connect to RabbitMQ
	log.Println("Connecting to RabbitMQ...")
	rmqConn, err := rmq.NewConnectionFromURL(config.RabbitMQURL)
	if err != nil {
		log.Fatalf("Failed to connect to RabbitMQ: %v", err)
	}
	defer rmqConn.Close()
	log.Println("Connected to RabbitMQ")

	// Create consumer manager
	consumerConfig := &consumer.ConsumerConfig{
		LogBufferTTL:     config.LogBufferTTL,
		LogBufferMaxMsgs: config.LogBufferMaxMsgs,
		DebugLogOutput:   config.DebugLogOutput,
	}
	consumerManager := consumer.NewManager(rmqConn, consumerConfig)
	defer consumerManager.Close()

	// Create gRPC server
	grpcServer := grpc.NewServer()
	logProcessorServer := server.NewServer(consumerManager)
	manmanpb.RegisterLogProcessorServer(grpcServer, logProcessorServer)

	// Start gRPC server
	addr := fmt.Sprintf("0.0.0.0:%s", config.GRPCPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}

	log.Printf("gRPC server listening on %s", addr)

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal, stopping server...")
		grpcServer.GracefulStop()
	}()

	// Start serving
	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}

	log.Println("Log-processor service stopped")
}
