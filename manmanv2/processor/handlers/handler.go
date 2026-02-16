package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/whale-net/everything/manmanv2/api/repository"
)

// EventHandler processes a single event type
type EventHandler interface {
	Handle(ctx context.Context, routingKey string, body []byte) error
}

// HandlerRegistry routes messages to appropriate handlers based on routing key patterns
type HandlerRegistry struct {
	repo     *repository.Repository
	handlers map[string]EventHandler
	logger   *slog.Logger
}

// NewHandlerRegistry creates a new handler registry
func NewHandlerRegistry(repo *repository.Repository, logger *slog.Logger) *HandlerRegistry {
	return &HandlerRegistry{
		repo:     repo,
		handlers: make(map[string]EventHandler),
		logger:   logger,
	}
}

// Register adds a handler for a routing key pattern (supports wildcards)
func (r *HandlerRegistry) Register(pattern string, handler EventHandler) {
	r.handlers[pattern] = handler
	r.logger.Info("registered handler", "pattern", pattern)
}

// Route routes a message to the appropriate handler
func (r *HandlerRegistry) Route(ctx context.Context, routingKey string, body []byte) error {
	for pattern, handler := range r.handlers {
		if matchRoutingKey(pattern, routingKey) {
			r.logger.Debug("routing message", "routing_key", routingKey, "pattern", pattern)
			return handler.Handle(ctx, routingKey, body)
		}
	}

	return fmt.Errorf("no handler found for routing key: %s", routingKey)
}

// matchRoutingKey checks if a routing key matches a pattern with wildcards
// Supports:
// - * matches exactly one word
// - # matches zero or more words
func matchRoutingKey(pattern, routingKey string) bool {
	patternParts := strings.Split(pattern, ".")
	keyParts := strings.Split(routingKey, ".")

	return matchParts(patternParts, keyParts)
}

func matchParts(pattern, key []string) bool {
	if len(pattern) == 0 {
		return len(key) == 0
	}

	if pattern[0] == "#" {
		// # matches zero or more words
		if len(pattern) == 1 {
			return true // # at end matches everything
		}
		// Try matching with zero words
		if matchParts(pattern[1:], key) {
			return true
		}
		// Try matching with one or more words
		if len(key) > 0 && matchParts(pattern, key[1:]) {
			return true
		}
		return false
	}

	if len(key) == 0 {
		return false
	}

	if pattern[0] == "*" || pattern[0] == key[0] {
		return matchParts(pattern[1:], key[1:])
	}

	return false
}
