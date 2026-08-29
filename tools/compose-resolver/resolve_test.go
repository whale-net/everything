package main

import (
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

func TestFindByRepository(t *testing.T) {
	entries := []*pb.EnvironmentStateEntry{
		{Artifact: &pb.Artifact{Repository: "ghcr.io/whale-net/manmanv2-control-api", Version: "v2.0.0"}},
		{Artifact: &pb.Artifact{Repository: "ghcr.io/whale-net/manmanv2-host-manager", Version: "v1.4.0", Digest: "sha256:abc"}},
	}

	got := findByRepository(entries, "ghcr.io/whale-net/manmanv2-host-manager")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got.Version != "v1.4.0" || got.Digest != "sha256:abc" {
		t.Errorf("got %+v, want version=v1.4.0 digest=sha256:abc", got)
	}
}

func TestFindByRepository_NoMatch(t *testing.T) {
	entries := []*pb.EnvironmentStateEntry{
		{Artifact: &pb.Artifact{Repository: "ghcr.io/whale-net/manmanv2-control-api", Version: "v2.0.0"}},
	}

	if got := findByRepository(entries, "ghcr.io/whale-net/manmanv2-host-manager"); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestFindByRepository_Empty(t *testing.T) {
	if got := findByRepository(nil, "ghcr.io/whale-net/manmanv2-host-manager"); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}
