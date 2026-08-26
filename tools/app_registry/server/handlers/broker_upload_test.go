package handlers

import (
	"context"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// setupBroker creates a test ArtifactServer.
func setupBroker(t *testing.T) *ArtifactServer {
	t.Helper()
	repo := fake.New()
	return NewArtifactServer(repo)
}

// TestBrokerUpload_Authorization tests FR-4: authorization requirement.
func TestBrokerUpload_Authorization_Unauthenticated(t *testing.T) {
	srv := setupBroker(t)
	ctx := context.Background()

	resp, err := srv.BrokerUpload(ctx, &pb.BrokerUploadRequest{
		ArtifactKind:     "binary",
		Version:          "v1.0.0",
		ArtifactIdentity: "test-identity",
		Files: []*pb.BrokerUploadFile{
			{VariantKey: "variant1", UncompressedDigest: "sha256:abc123"},
		},
	})

	if err == nil {
		t.Fatal("expected authorization error for unauthenticated caller")
	}
	if resp != nil {
		t.Fatal("expected nil response for unauthenticated caller")
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated code, got %v", st.Code())
	}
}

// TestBrokerUpload_ValidationErrors tests request validation.
func TestBrokerUpload_ValidationErrors(t *testing.T) {
	srv := setupBroker(t)
	ctx := authedCtx()

	cases := []struct {
		name    string
		request *pb.BrokerUploadRequest
		wantErr bool
	}{
		{
			name: "missing_artifact_kind",
			request: &pb.BrokerUploadRequest{
				ArtifactKind:     "",
				Version:          "v1.0.0",
				ArtifactIdentity: "test",
				Files: []*pb.BrokerUploadFile{
					{VariantKey: "v1", UncompressedDigest: "sha256:test"},
				},
			},
			wantErr: true,
		},
		{
			name: "missing_version",
			request: &pb.BrokerUploadRequest{
				ArtifactKind:     "binary",
				Version:          "",
				ArtifactIdentity: "test",
				Files: []*pb.BrokerUploadFile{
					{VariantKey: "v1", UncompressedDigest: "sha256:test"},
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := srv.BrokerUpload(ctx, tc.request)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if resp != nil {
					t.Fatal("expected nil response on error")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp == nil {
					t.Fatal("expected non-nil response")
				}
			}
		})
	}
}
