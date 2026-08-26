package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// ConfigAckWaiter manages bounded waits for config acknowledgements.
// It coordinates between API requests and AMQP ack signals to deliver
// results to waiting clients within the specified deadline.
type ConfigAckWaiter struct {
	mu sync.RWMutex
	// waiters maps (board_id, version) -> list of channels to notify
	waiters map[string]map[int64][]*ackResult
}

// ackResult holds the result of waiting for an ack.
type ackResult struct {
	resolution pb.ConfigAckResolution
	reason     string
	ackedAt    int64
	done       chan struct{}
}

// NewConfigAckWaiter creates a new waiter for managing bounded waits.
func NewConfigAckWaiter() *ConfigAckWaiter {
	return &ConfigAckWaiter{
		waiters: make(map[string]map[int64][]*ackResult),
	}
}

// Wait blocks until an ack arrives for (board_id, version) or the deadline is reached.
// It returns the resolution and rejection reason (if rejected).
// The deadline is clamped to max 30 seconds from now.
func (w *ConfigAckWaiter) Wait(ctx context.Context, boardID int64, version int64, deadline time.Time) (pb.ConfigAckResolution, string, error) {
	// Clamp deadline to 30 seconds from now
	now := time.Now()
	maxDeadline := now.Add(30 * time.Second)
	if deadline.After(maxDeadline) {
		deadline = maxDeadline
	}

	// If deadline has already passed, return immediately
	if deadline.Before(now) {
		return pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_STILL_PENDING, "", nil
	}

	// Create a result channel for this waiter
	result := &ackResult{
		done: make(chan struct{}),
	}

	// Register this waiter
	w.mu.Lock()
	boardKey := fmt.Sprintf("%d", boardID)
	if w.waiters[boardKey] == nil {
		w.waiters[boardKey] = make(map[int64][]*ackResult)
	}
	w.waiters[boardKey][version] = append(w.waiters[boardKey][version], result)
	w.mu.Unlock()

	// Clean up on exit
	defer func() {
		w.mu.Lock()
		if waiters, ok := w.waiters[boardKey]; ok {
			newWaiters := make([]*ackResult, 0, len(waiters[version]))
			for _, w := range waiters[version] {
				if w != result {
					newWaiters = append(newWaiters, w)
				}
			}
			if len(newWaiters) == 0 {
				delete(waiters, version)
				if len(waiters) == 0 {
					delete(w.waiters, boardKey)
				}
			} else {
				waiters[version] = newWaiters
			}
		}
		w.mu.Unlock()
	}()

	// Wait for either the ack to arrive or the deadline to pass
	select {
	case <-result.done:
		return result.resolution, result.reason, nil
	case <-time.After(time.Until(deadline)):
		return pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_STILL_PENDING, "", nil
	case <-ctx.Done():
		return pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_UNSPECIFIED, "", ctx.Err()
	}
}

// NotifyAck notifies all waiters for a (device_id, version) pair that an ack has arrived.
// This is called by the ack listener when a signal is received.
func (w *ConfigAckWaiter) NotifyAck(boardID int64, version int64, accepted bool, reason string, ackedAt time.Time) {
	w.mu.RLock()
	boardKey := fmt.Sprintf("%d", boardID)
	waiters, ok := w.waiters[boardKey]
	if !ok {
		w.mu.RUnlock()
		return
	}
	results, ok := waiters[version]
	if !ok {
		w.mu.RUnlock()
		return
	}

	// Copy the list so we can release the lock before notifying
	resultsCopy := make([]*ackResult, len(results))
	copy(resultsCopy, results)
	w.mu.RUnlock()

	// Notify all waiters
	for _, result := range resultsCopy {
		if accepted {
			result.resolution = pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_ACCEPTED
		} else {
			result.resolution = pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_REJECTED
			result.reason = reason
		}
		result.ackedAt = ackedAt.Unix()
		close(result.done)
	}
}
