package grpc_test

import (
	"context"
	"testing"
	"time"

	"github.com/whale-net/everything/manman/host/grpc"
)

func TestNewClient_InvalidAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Test with invalid address (should fail quickly)
	_, err := grpc.NewClient(ctx, "invalid-address:50051")
	if err == nil {
		t.Error("Expected error for invalid address, got nil")
	}
}

func TestClient_Close(t *testing.T) {
	// Test that Close doesn't panic on nil client
	client := &grpc.Client{}
	err := client.Close()
	if err != nil {
		// Close on uninitialized client might return error, that's OK
		_ = err
	}
}

func TestClient_GetConnection_Nil(t *testing.T) {
	// Test that GetConnection handles nil gracefully
	client := &grpc.Client{}
	conn := client.GetConnection()
	if conn != nil {
		t.Error("Expected nil connection for uninitialized client")
	}
}
