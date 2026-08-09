package cmd

import (
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

func TestDiffEnvironmentStates_BothEmpty(t *testing.T) {
	a := &pb.GetEnvironmentStateResponse{}
	b := &pb.GetEnvironmentStateResponse{}

	d := diffEnvironmentStates("dev", "prod", a, b)

	if d.EnvironmentA != "dev" || d.EnvironmentB != "prod" {
		t.Fatalf("unexpected environment labels: %+v", d)
	}
	if len(d.Entries) != 0 {
		t.Fatalf("expected no entries for two empty states, got %+v", d.Entries)
	}
}

func TestDiffEnvironmentStates_OnlyInA(t *testing.T) {
	a := &pb.GetEnvironmentStateResponse{
		Entries: []*pb.EnvironmentStateEntry{
			{
				Promotion: &pb.Promotion{AppId: "app-1"},
				Artifact: &pb.Artifact{
					Kind:       pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
					Repository: "ghcr.io/whale-net/demo-app",
					Version:    "v1.0.0",
					Digest:     "sha256:aaa",
				},
			},
		},
	}
	b := &pb.GetEnvironmentStateResponse{}

	d := diffEnvironmentStates("dev", "prod", a, b)

	if len(d.Entries) != 1 {
		t.Fatalf("expected exactly one entry, got %+v", d.Entries)
	}
	e := d.Entries[0]
	if e.Status != "only_a" {
		t.Errorf("status = %q, want only_a", e.Status)
	}
	if e.Target != "image:app-1" {
		t.Errorf("target = %q, want image:app-1", e.Target)
	}
	if e.VersionA != "v1.0.0" || e.DigestA != "sha256:aaa" {
		t.Errorf("unexpected A-side fields: %+v", e)
	}
	if e.VersionB != "" || e.DigestB != "" {
		t.Errorf("expected empty B-side fields, got %+v", e)
	}
}

func TestDiffEnvironmentStates_OnlyInB(t *testing.T) {
	a := &pb.GetEnvironmentStateResponse{}
	b := &pb.GetEnvironmentStateResponse{
		Entries: []*pb.EnvironmentStateEntry{
			{
				Promotion: &pb.Promotion{ChartId: "chart-1"},
				Artifact: &pb.Artifact{
					Kind:       pb.ArtifactKind_ARTIFACT_KIND_CHART,
					Repository: "https://charts.whalenet.dev/demo-app",
					Version:    "v2.0.0",
					Digest:     "sha256:bbb",
				},
			},
		},
	}

	d := diffEnvironmentStates("dev", "prod", a, b)

	if len(d.Entries) != 1 {
		t.Fatalf("expected exactly one entry, got %+v", d.Entries)
	}
	e := d.Entries[0]
	if e.Status != "only_b" {
		t.Errorf("status = %q, want only_b", e.Status)
	}
	if e.Target != "chart:chart-1" {
		t.Errorf("target = %q, want chart:chart-1", e.Target)
	}
	if e.Kind != "chart" {
		t.Errorf("kind = %q, want chart", e.Kind)
	}
	if e.VersionB != "v2.0.0" || e.DigestB != "sha256:bbb" {
		t.Errorf("unexpected B-side fields: %+v", e)
	}
	if e.VersionA != "" || e.DigestA != "" {
		t.Errorf("expected empty A-side fields, got %+v", e)
	}
}

func TestDiffEnvironmentStates_DifferentDigest(t *testing.T) {
	entry := func(digest, version string) *pb.EnvironmentStateEntry {
		return &pb.EnvironmentStateEntry{
			Promotion: &pb.Promotion{AppId: "app-1"},
			Artifact: &pb.Artifact{
				Kind:       pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
				Repository: "ghcr.io/whale-net/demo-app",
				Version:    version,
				Digest:     digest,
			},
		}
	}
	a := &pb.GetEnvironmentStateResponse{Entries: []*pb.EnvironmentStateEntry{entry("sha256:aaa", "v1.0.0")}}
	b := &pb.GetEnvironmentStateResponse{Entries: []*pb.EnvironmentStateEntry{entry("sha256:bbb", "v1.1.0")}}

	d := diffEnvironmentStates("dev", "prod", a, b)

	if len(d.Entries) != 1 {
		t.Fatalf("expected exactly one entry, got %+v", d.Entries)
	}
	e := d.Entries[0]
	if e.Status != "different" {
		t.Errorf("status = %q, want different", e.Status)
	}
	if e.VersionA != "v1.0.0" || e.VersionB != "v1.1.0" {
		t.Errorf("unexpected versions: %+v", e)
	}
	if e.DigestA != "sha256:aaa" || e.DigestB != "sha256:bbb" {
		t.Errorf("unexpected digests: %+v", e)
	}
}

func TestDiffEnvironmentStates_SameDigestOmitted(t *testing.T) {
	entry := func() *pb.EnvironmentStateEntry {
		return &pb.EnvironmentStateEntry{
			Promotion: &pb.Promotion{AppId: "app-1"},
			Artifact: &pb.Artifact{
				Kind:       pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
				Repository: "ghcr.io/whale-net/demo-app",
				Version:    "v1.0.0",
				Digest:     "sha256:aaa",
			},
		}
	}
	a := &pb.GetEnvironmentStateResponse{Entries: []*pb.EnvironmentStateEntry{entry()}}
	b := &pb.GetEnvironmentStateResponse{Entries: []*pb.EnvironmentStateEntry{entry()}}

	d := diffEnvironmentStates("dev", "prod", a, b)

	if len(d.Entries) != 0 {
		t.Fatalf("expected identical entries to be omitted, got %+v", d.Entries)
	}
}

func TestPromoteIdempotencyKey_GeneratesWhenEmpty(t *testing.T) {
	k1 := promoteIdempotencyKey("")
	k2 := promoteIdempotencyKey("")
	if k1 == "" || k2 == "" {
		t.Fatal("expected a non-empty generated key")
	}
	if k1 == k2 {
		t.Fatal("expected distinct generated keys across calls")
	}
}

func TestPromoteIdempotencyKey_PreservesProvided(t *testing.T) {
	if got := promoteIdempotencyKey("explicit-key"); got != "explicit-key" {
		t.Fatalf("got %q, want explicit-key", got)
	}
}
