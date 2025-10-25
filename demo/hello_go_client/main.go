package main

import (
	"fmt"

	client "github.com/whale-net/everything/generated/go/demo/hello_fastapi_go"
)

func main() {
	// Create a new API client configuration
	cfg := client.NewConfiguration()
	cfg.Host = "localhost:8000"
	cfg.Scheme = "http"

	// Create a new API client
	apiClient := client.NewAPIClient(cfg)

	fmt.Printf("Go OpenAPI client successfully imported and initialized!\n")
	fmt.Printf("Client configured for: %s://%s\n", cfg.Scheme, cfg.Host)
	fmt.Printf("API client type: %T\n", apiClient)
}
