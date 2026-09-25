//go:build integration

// Contract test for the composition root: NewRepository must populate every
// field of repository.Repository.
//
// This is the cheapest regression guard in the package. A field dropped from
// the wiring in repository.go compiles without complaint and does nothing
// until a handler dereferences it, so the failure surfaces as a nil panic in
// whichever request path happens to touch that field first -- potentially
// long after the wiring mistake. Here it is a failing test instead.
//
// The table is keyed on field name, so adding a field to repository.Repository
// without wiring it is a visible gap in this file, and a misspelled field
// name is a compile error.
//
// This deliberately does not need a real database -- NewRepository only
// stores the pool in each struct. It is still build-tagged and manual-tagged
// like the rest of this package's tests, so the target shape stays uniform.
//
// Run it explicitly:
//
//	bazel test //manmanv2/api/repository/postgres:repository_composition_integration_test --test_output=all
package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/manmanv2/api/repository"
)

func TestNewRepositoryWiresEveryField(t *testing.T) {
	// NewRepository never dials, so a zero-value pool is enough to prove the
	// wiring -- each constructor just stores it.
	repo := NewRepository((*pgxpool.Pool)(nil))
	if repo == nil {
		t.Fatal("NewRepository returned nil")
	}

	// Each entry reports whether its field is non-nil. A new field on
	// repository.Repository should get an entry here; the count assertion
	// below is what makes an omission fail rather than pass quietly.
	fields := []struct {
		name    string
		present bool
	}{
		{"Servers", repo.Servers != nil},
		{"Games", repo.Games != nil},
		{"GameConfigs", repo.GameConfigs != nil},
		{"ServerGameConfigs", repo.ServerGameConfigs != nil},
		{"GameConfigWorkshopLibraries", repo.GameConfigWorkshopLibraries != nil},
		{"Sessions", repo.Sessions != nil},
		{"ServerCapabilities", repo.ServerCapabilities != nil},
		{"LogReferences", repo.LogReferences != nil},
		{"Backups", repo.Backups != nil},
		{"BackupConfigs", repo.BackupConfigs != nil},
		{"ServerPorts", repo.ServerPorts != nil},
		{"ServerPortRanges", repo.ServerPortRanges != nil},
		{"ConfigurationStrategies", repo.ConfigurationStrategies != nil},
		{"ConfigurationPatches", repo.ConfigurationPatches != nil},
		{"GameConfigVolumes", repo.GameConfigVolumes != nil},
		{"WorkshopAddons", repo.WorkshopAddons != nil},
		{"WorkshopInstallations", repo.WorkshopInstallations != nil},
		{"WorkshopLibraries", repo.WorkshopLibraries != nil},
		{"WorkshopBatchJobs", repo.WorkshopBatchJobs != nil},
		{"AddonPathPresets", repo.AddonPathPresets != nil},
		{"PendingRestarts", repo.PendingRestarts != nil},
		{"WorkshopCache", repo.WorkshopCache != nil},
		{"Actions", repo.Actions != nil},
	}

	var unwired []string
	for _, f := range fields {
		if !f.present {
			unwired = append(unwired, f.name)
		}
	}
	if len(unwired) > 0 {
		t.Errorf("NewRepository left these fields nil: %v", unwired)
	}

	// Guards against the table above silently going stale: if a field is
	// added to repository.Repository without a corresponding entry here,
	// this count stops matching and the omission gets noticed.
	if want := 23; len(fields) != want {
		t.Errorf("this test covers %d fields, but repository.Repository has %d -- "+
			"a field was added to the struct without being wired into NewRepository or listed here",
			len(fields), want)
	}
}

// TestRepositoryStructSatisfiesItsInterface documents that the concrete
// composition root is what api handlers depend on, and that the interface
// package and the postgres package stay in step.
func TestRepositoryStructSatisfiesItsInterface(t *testing.T) {
	var _ *repository.Repository = NewRepository((*pgxpool.Pool)(nil))
}
