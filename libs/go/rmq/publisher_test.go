package rmq_test

import (
	"context"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
)

func TestPublisher_Publish_String(t *testing.T) {
	// This test would require a real RabbitMQ instance
	t.Skip("Requires RabbitMQ instance")
	
	config := rmq.Config{
		Host:     "localhost",
		Port:     5672,
		Username: "guest",
		Password: "guest",
		VHost:    "/",
	}
	
	conn, err := rmq.NewConnection(config)
	if err != nil {
		t.Fatalf("Failed to create connection: %v", err)
	}
	defer conn.Close()
	
	publisher, err := rmq.NewPublisher(conn)
	if err != nil {
		t.Fatalf("Failed to create publisher: %v", err)
	}
	defer publisher.Close()
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	err = publisher.Publish(ctx, "test-exchange", "test.key", "test message")
	if err != nil {
		t.Errorf("Failed to publish message: %v", err)
	}
}

func TestPublisher_Publish_JSON(t *testing.T) {
	// This test would require a real RabbitMQ instance
	t.Skip("Requires RabbitMQ instance")
	
	type TestMessage struct {
		ID      int    `json:"id"`
		Message string `json:"message"`
	}
	
	config := rmq.Config{
		Host:     "localhost",
		Port:     5672,
		Username: "guest",
		Password: "guest",
		VHost:    "/",
	}
	
	conn, err := rmq.NewConnection(config)
	if err != nil {
		t.Fatalf("Failed to create connection: %v", err)
	}
	defer conn.Close()
	
	publisher, err := rmq.NewPublisher(conn)
	if err != nil {
		t.Fatalf("Failed to create publisher: %v", err)
	}
	defer publisher.Close()
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	msg := TestMessage{
		ID:      1,
		Message: "test",
	}
	
	err = publisher.Publish(ctx, "test-exchange", "test.key", msg)
	if err != nil {
		t.Errorf("Failed to publish JSON message: %v", err)
	}
}

func TestPublisher_Close(t *testing.T) {
	// Test that Close doesn't panic on nil publisher
	publisher := &rmq.Publisher{}
	err := publisher.Close()
	if err != nil {
		// Close on uninitialized publisher might return error, that's OK
		_ = err
	}
}
