package rmq_test

import (
	"context"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
)

func TestConsumer_BindExchange(t *testing.T) {
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
	
	consumer, err := rmq.NewConsumer(conn, "test-queue")
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}
	defer consumer.Close()
	
	routingKeys := []string{"test.key", "test.*", "test.#"}
	err = consumer.BindExchange("test-exchange", routingKeys)
	if err != nil {
		t.Errorf("Failed to bind exchange: %v", err)
	}
}

func TestConsumer_RegisterHandler(t *testing.T) {
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
	
	consumer, err := rmq.NewConsumer(conn, "test-queue")
	if err != nil {
		t.Fatalf("Failed to create consumer: %v", err)
	}
	defer consumer.Close()
	
	handler := func(ctx context.Context, msg rmq.Message) error {
		return nil
	}
	
	consumer.RegisterHandler("test.key", handler)
	
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	
	err = consumer.Start(ctx)
	if err != nil {
		t.Errorf("Failed to start consumer: %v", err)
	}
}

func TestUnmarshalMessage(t *testing.T) {
	type TestStruct struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	
	jsonData := `{"id":1,"name":"test"}`
	
	var result TestStruct
	err := rmq.UnmarshalMessage([]byte(jsonData), &result)
	if err != nil {
		t.Fatalf("Failed to unmarshal message: %v", err)
	}
	
	if result.ID != 1 {
		t.Errorf("Expected ID 1, got %d", result.ID)
	}
	if result.Name != "test" {
		t.Errorf("Expected name 'test', got %s", result.Name)
	}
}

func TestUnmarshalMessage_InvalidJSON(t *testing.T) {
	type TestStruct struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	
	invalidJSON := `{"id":1,"name":}`
	
	var result TestStruct
	err := rmq.UnmarshalMessage([]byte(invalidJSON), &result)
	if err == nil {
		t.Error("Expected error for invalid JSON, got nil")
	}
}
