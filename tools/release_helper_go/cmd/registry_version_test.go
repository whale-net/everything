package cmd

import (
	"context"
	"errors"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestResolveVersion_NilClientUsesTagFallback(t *testing.T) {
	called := false
	version, fromRegistry, err := resolveVersion(context.Background(), nil, pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "demo-hello-go", "patch", "key-1",
		func() (string, error) { called = true; return "v1.2.3", nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected tagFallback to be called when client is nil")
	}
	if fromRegistry {
		t.Error("expected fromRegistry=false when client is nil")
	}
	if version != "v1.2.3" {
		t.Errorf("got version %q, want v1.2.3", version)
	}
}

func TestResolveVersion_SuccessUsesRegistryNotTags(t *testing.T) {
	fake := &FakeArtifactRegistryClient{
		AllocateVersionFn: func(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error) {
			return &pb.AllocateVersionResponse{Version: "v3.0.0", PreviousVersion: "v2.5.0"}, nil
		},
	}
	tagCalled := false
	version, fromRegistry, err := resolveVersion(context.Background(), fake, pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "app-registry-app-registry", "major", "key-2",
		func() (string, error) { tagCalled = true; return "v0.0.1", nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tagCalled {
		t.Error("tagFallback must not be called when AllocateVersion succeeds")
	}
	if !fromRegistry {
		t.Error("expected fromRegistry=true on AllocateVersion success")
	}
	if version != "v3.0.0" {
		t.Errorf("got version %q, want v3.0.0", version)
	}
	if len(fake.AllocateVersionCalls) != 1 {
		t.Fatalf("expected 1 AllocateVersion call, got %d", len(fake.AllocateVersionCalls))
	}
	req := fake.AllocateVersionCalls[0]
	if req.OwnerFullName != "app-registry-app-registry" || req.Increment != "major" || req.IdempotencyKey != "key-2" {
		t.Errorf("unexpected request: %+v", req)
	}
}

func TestResolveVersion_NotAllocatedFallsBackToTags(t *testing.T) {
	fake := &FakeArtifactRegistryClient{
		AllocateVersionFn: func(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, `domain "demo" is at adoption stage "observe"; AllocateVersion only serves domains at stage "allocate"`)
		},
	}
	tagCalled := false
	version, fromRegistry, err := resolveVersion(context.Background(), fake, pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "demo-hello-go", "patch", "key-3",
		func() (string, error) { tagCalled = true; return "v1.0.1", nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tagCalled {
		t.Error("expected tagFallback to be called when domain is not at stage allocate")
	}
	if fromRegistry {
		t.Error("expected fromRegistry=false on FailedPrecondition fallback")
	}
	if version != "v1.0.1" {
		t.Errorf("got version %q, want v1.0.1", version)
	}
}

func TestResolveVersion_OtherRegistryErrorIsFatal(t *testing.T) {
	fake := &FakeArtifactRegistryClient{
		AllocateVersionFn: func(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error) {
			return nil, status.Error(codes.Unavailable, "registry down")
		},
	}
	tagCalled := false
	_, _, err := resolveVersion(context.Background(), fake, pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "app-registry-app-registry", "patch", "key-4",
		func() (string, error) { tagCalled = true; return "v9.9.9", nil })
	if err == nil {
		t.Fatal("expected a fatal error, got nil")
	}
	if tagCalled {
		t.Error("tagFallback must NEVER be called on a non-FailedPrecondition registry error -- an allocate domain must fail loudly, not silently revert to tags (issue #829)")
	}
}

func TestDialVersioningClient_ReturnsInjectedClientWithoutDialing(t *testing.T) {
	injected := &FakeArtifactRegistryClient{}
	client, closeFn, err := dialVersioningClient(context.Background(), injected)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client != injected {
		t.Error("expected the injected client to be returned unchanged")
	}
	if closeErr := closeFn(); closeErr != nil {
		t.Errorf("unexpected close error: %v", closeErr)
	}
}

func TestDialVersioningClient_DialFailureIsFatal(t *testing.T) {
	old := defaultArtifactRegistryClientFactory
	defaultArtifactRegistryClientFactory = func(ctx context.Context) (pb.ArtifactRegistryClient, func() error, error) {
		return nil, nil, errors.New("dial tcp: connection refused")
	}
	defer func() { defaultArtifactRegistryClientFactory = old }()

	_, _, err := dialVersioningClient(context.Background(), nil)
	if err == nil {
		t.Fatal("expected dial failure to be returned as an error, not silently swallowed")
	}
}
