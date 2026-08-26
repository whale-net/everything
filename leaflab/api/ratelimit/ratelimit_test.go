package ratelimit

import (
	"testing"
)

func TestKeyForPrincipal(t *testing.T) {
	key := ForPrincipal("device-12345")
	if key.Principal() != "device-12345" {
		t.Errorf("expected principal device-12345, got %s", key.Principal())
	}
	if key.HasSession() {
		t.Error("expected no session, got session")
	}
}

func TestKeyForSession(t *testing.T) {
	key := ForSession("device-12345", "session-abc")
	if key.Principal() != "device-12345" {
		t.Errorf("expected principal device-12345, got %s", key.Principal())
	}
	if !key.HasSession() {
		t.Error("expected session, got no session")
	}
}

func TestRegistryRegisterAndGet(t *testing.T) {
	reg := NewRegistry()

	bucket := Bucket{
		Name:              "read",
		RequestsPerSecond: 100,
		Description:       "Rate limit for read operations",
	}

	reg.Register(bucket)

	retrieved, ok := reg.Get("read")
	if !ok {
		t.Error("expected bucket to be found")
	}
	if retrieved.RequestsPerSecond != 100 {
		t.Errorf("expected 100 requests per second, got %d", retrieved.RequestsPerSecond)
	}
}

func TestRegistryGetNotFound(t *testing.T) {
	reg := NewRegistry()

	_, ok := reg.Get("nonexistent")
	if ok {
		t.Error("expected bucket not to be found")
	}
}
