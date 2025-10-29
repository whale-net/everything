// Demo application showing observability library usage
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/whale-net/everything/libs/go/observability"
)

func main() {
	// Configure observability with auto-detection from environment
	fmt.Println("Configuring observability...")
	if err := observability.ConfigureAll(); err != nil {
		log.Fatalf("Failed to configure observability: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		observability.ShutdownAll(ctx)
	}()

	logger := observability.DefaultLogger()
	logger.Info("Demo application started",
		"demo", "observability",
		"features", "logging+tracing",
	)

	// Simulate processing requests
	for i := 0; i < 5; i++ {
		processRequest(context.Background(), i+1)
		time.Sleep(500 * time.Millisecond)
	}

	logger.Info("Demo completed")
}

func processRequest(ctx context.Context, requestNum int) {
	// Start a trace span
	ctx, span := observability.StartSpanWithContext(ctx, "process-request")
	defer span.End()

	logger := observability.DefaultLogger()

	// Create observability context with request details
	obsCtx := observability.NewContext()
	obsCtx.RequestID = fmt.Sprintf("req-%d", requestNum)
	obsCtx.UserID = fmt.Sprintf("user-%d", rand.Intn(100))
	obsCtx.HTTPMethod = "POST"
	obsCtx.HTTPPath = "/api/orders"
	obsCtx.ClientIP = "192.168.1.100"
	obsCtx.Operation = "create-order"

	// Add custom attributes
	obsCtx.Custom["order_type"] = "express"
	obsCtx.Custom["priority"] = rand.Intn(3) + 1

	// Add context to span
	ctx = observability.WithContext(ctx, obsCtx)

	// Log with context (automatically includes request_id, user_id, etc.)
	logger.InfoContext(ctx, "Processing request")

	// Simulate validation
	validateOrder(ctx)

	// Simulate database operation
	saveOrder(ctx)

	// Set status code
	obsCtx.HTTPStatusCode = 201
	logger.InfoContext(ctx, "Request completed successfully")
}

func validateOrder(ctx context.Context) {
	ctx, span := observability.StartSpan(ctx, "validate-order")
	defer span.End()

	logger := observability.DefaultLogger()

	logger.DebugContext(ctx, "Validating order data")

	// Simulate validation work
	time.Sleep(50 * time.Millisecond)

	logger.InfoContext(ctx, "Order validated")
}

func saveOrder(ctx context.Context) {
	ctx, span := observability.StartSpan(ctx, "save-order")
	defer span.End()

	logger := observability.DefaultLogger()

	logger.DebugContext(ctx, "Saving order to database")

	// Simulate database work
	time.Sleep(100 * time.Millisecond)

	orderID := fmt.Sprintf("ord-%d", rand.Intn(10000))
	logger.InfoContext(ctx, "Order saved", "order_id", orderID)
}
