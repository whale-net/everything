package postgres

import (
	"testing"
)

// TestServerRepository_HostPublicAddress mirrors the pattern used by the
// workshop_*_property_test.go files in this package: it requires a live
// PostgreSQL connection (no such fixture is wired into this Bazel target),
// so it is skipped by default and documents the coverage this task requires.
//
// NOTE: This test is skipped because it requires a live database connection.
// To run this test, you need to:
// 1. Start a PostgreSQL database (e.g., via Tilt) with migrations applied
//    through 035_add_server_host_public_address.
// 2. Set the DATABASE_URL environment variable.
// 3. Remove the t.Skip() call and wire up a real *pgxpool.Pool.
func TestServerRepository_HostPublicAddress(t *testing.T) {
	t.Skip("Skipping repository test - requires live database connection")

	// Intended coverage (see manmanv2/api/handlers/server_test.go for the
	// equivalent behavior exercised against a mock repository):
	//
	// ctx := context.Background()
	// db := setupTestDatabase(t)
	// defer cleanupTestDatabase(t, db)
	// repo := NewServerRepository(db)
	//
	// // A newly created server has a NULL host_public_address.
	// created, err := repo.Create(ctx, "test-server")
	// if err != nil {
	// 	t.Fatalf("Create failed: %v", err)
	// }
	// if created.HostPublicAddress != nil {
	// 	t.Errorf("Expected nil HostPublicAddress on create, got: %v", *created.HostPublicAddress)
	// }
	//
	// // Update writes the address.
	// addr := "203.0.113.5"
	// created.HostPublicAddress = &addr
	// if err := repo.Update(ctx, created); err != nil {
	// 	t.Fatalf("Update failed: %v", err)
	// }
	//
	// // Get reads it back.
	// fetched, err := repo.Get(ctx, created.ServerID)
	// if err != nil {
	// 	t.Fatalf("Get failed: %v", err)
	// }
	// if fetched.HostPublicAddress == nil || *fetched.HostPublicAddress != addr {
	// 	t.Errorf("Expected HostPublicAddress=%q, got: %v", addr, fetched.HostPublicAddress)
	// }
	//
	// // List reads it back too.
	// listed, err := repo.List(ctx, 50, 0)
	// if err != nil {
	// 	t.Fatalf("List failed: %v", err)
	// }
	// found := false
	// for _, s := range listed {
	// 	if s.ServerID == created.ServerID {
	// 		found = true
	// 		if s.HostPublicAddress == nil || *s.HostPublicAddress != addr {
	// 			t.Errorf("Expected HostPublicAddress=%q in List, got: %v", addr, s.HostPublicAddress)
	// 		}
	// 	}
	// }
	// if !found {
	// 	t.Error("Expected created server to appear in List results")
	// }
	//
	// // Update clears the address back to NULL.
	// fetched.HostPublicAddress = nil
	// if err := repo.Update(ctx, fetched); err != nil {
	// 	t.Fatalf("Update (clear) failed: %v", err)
	// }
	// cleared, err := repo.Get(ctx, created.ServerID)
	// if err != nil {
	// 	t.Fatalf("Get after clear failed: %v", err)
	// }
	// if cleared.HostPublicAddress != nil {
	// 	t.Errorf("Expected nil HostPublicAddress after clear, got: %v", *cleared.HostPublicAddress)
	// }
}
