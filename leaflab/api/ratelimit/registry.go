package ratelimit

import (
	"sync"
)

// Bucket represents a named rate limit bucket with a maximum request count per time window.
// Buckets are configuration objects that define the rate limit parameters.
type Bucket struct {
	// Name is the unique identifier for this bucket (e.g., "read", "claim-initiate", "challenge").
	Name string
	// RequestsPerSecond is the rate limit in requests per second.
	RequestsPerSecond int
	// Description is a human-readable description of when this bucket is used.
	Description string
}

// Registry holds named rate limit buckets and allows later phases to register and look up limits.
type Registry struct {
	mu      sync.RWMutex
	buckets map[string]Bucket
}

// NewRegistry creates a new empty bucket registry.
func NewRegistry() *Registry {
	return &Registry{
		buckets: make(map[string]Bucket),
	}
}

// Register adds or updates a named bucket in the registry.
// If a bucket with the same name already exists, it is replaced.
func (r *Registry) Register(bucket Bucket) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buckets[bucket.Name] = bucket
}

// Get retrieves a bucket by name.
// Returns the bucket and true if found, or an empty Bucket and false if not found.
func (r *Registry) Get(name string) (Bucket, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.buckets[name]
	return b, ok
}

// All returns a copy of all registered buckets.
func (r *Registry) All() map[string]Bucket {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]Bucket, len(r.buckets))
	for k, v := range r.buckets {
		result[k] = v
	}
	return result
}
