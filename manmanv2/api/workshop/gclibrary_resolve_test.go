package workshop

import (
	"context"
	"fmt"
	"testing"

	"github.com/whale-net/everything/manmanv2/models"
)

// fakeLibraryRepoForResolve is a minimal WorkshopLibraryRepository double
// backing ResolveGameConfigLibraryAddons' shared resolveAddonIDsFromLibraries
// BFS (M6 #2365, plan #2359): only ListAddons and ListReferences are
// exercised, everything else fails loudly if a test accidentally reaches it.
type fakeLibraryRepoForResolve struct {
	addonsByLibrary     map[int64][]*manman.WorkshopAddonWithGame
	referencesByLibrary map[int64][]*manman.WorkshopLibrary
}

func (f *fakeLibraryRepoForResolve) Create(ctx context.Context, library *manman.WorkshopLibrary) (*manman.WorkshopLibrary, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeLibraryRepoForResolve) Get(ctx context.Context, libraryID int64) (*manman.WorkshopLibrary, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeLibraryRepoForResolve) List(ctx context.Context, gameID *int64, limit, offset int) ([]*manman.WorkshopLibrary, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeLibraryRepoForResolve) Update(ctx context.Context, library *manman.WorkshopLibrary) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeLibraryRepoForResolve) Delete(ctx context.Context, libraryID int64) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeLibraryRepoForResolve) AddAddon(ctx context.Context, libraryID, addonID int64, displayOrder int) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeLibraryRepoForResolve) RemoveAddon(ctx context.Context, libraryID, addonID int64) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeLibraryRepoForResolve) ListAddons(ctx context.Context, libraryID int64) ([]*manman.WorkshopAddonWithGame, error) {
	return f.addonsByLibrary[libraryID], nil
}

func (f *fakeLibraryRepoForResolve) AddReference(ctx context.Context, parentLibraryID, childLibraryID int64) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeLibraryRepoForResolve) RemoveReference(ctx context.Context, parentLibraryID, childLibraryID int64) error {
	return fmt.Errorf("not implemented")
}

func (f *fakeLibraryRepoForResolve) ListReferences(ctx context.Context, libraryID int64) ([]*manman.WorkshopLibrary, error) {
	return f.referencesByLibrary[libraryID], nil
}

func (f *fakeLibraryRepoForResolve) DetectCircularReference(ctx context.Context, parentLibraryID, childLibraryID int64) (bool, error) {
	return false, fmt.Errorf("not implemented")
}

// fakeGCLibraryRepoForResolve is a minimal
// repository.GameConfigWorkshopLibraryRepository double: only ListLibraries
// (what ResolveGameConfigLibraryAddons reads) and RemoveLibrary (the single
// write FR9's detach performs -- the same call
// RemoveLibraryFromGameConfig's handler delegates to) are exercised here.
// The conflict-resolution methods are covered at the repository layer
// (postgres/gameconfig_workshop_library_integration_test.go) and the
// handler layer (handlers/workshop/gameconfig_test.go); this fake exists
// purely to prove the manager-level resolution helper's FR8/FR9 semantics
// against a shared attachment store.
type fakeGCLibraryRepoForResolve struct {
	librariesByConfig map[int64][]*manman.WorkshopLibrary // configID -> attached libraries
}

func (f *fakeGCLibraryRepoForResolve) ListLibraries(ctx context.Context, configID int64) ([]*manman.WorkshopLibrary, error) {
	return f.librariesByConfig[configID], nil
}

func (f *fakeGCLibraryRepoForResolve) ListAttachments(ctx context.Context, configID int64) ([]*manman.GameConfigWorkshopLibrary, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeGCLibraryRepoForResolve) AddLibrary(ctx context.Context, configID, libraryID int64, presetID, volumeID *int64, installationPathOverride *string) error {
	return fmt.Errorf("not implemented")
}

// RemoveLibrary mirrors the real repository's write: it removes libraryID
// from configID's attached set. There is no per-SGC bookkeeping here on
// purpose -- every deployment resolves through ListLibraries above, so this
// single write is FR9's entire detach fan-out.
func (f *fakeGCLibraryRepoForResolve) RemoveLibrary(ctx context.Context, configID, libraryID int64) error {
	kept := f.librariesByConfig[configID][:0]
	for _, lib := range f.librariesByConfig[configID] {
		if lib.LibraryID != libraryID {
			kept = append(kept, lib)
		}
	}
	f.librariesByConfig[configID] = kept
	return nil
}

func (f *fakeGCLibraryRepoForResolve) ListUnresolvedConflicts(ctx context.Context) ([]*manman.WorkshopLibraryMigrationConflict, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeGCLibraryRepoForResolve) GetConflictForConfig(ctx context.Context, configID int64) (*manman.WorkshopLibraryMigrationConflict, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeGCLibraryRepoForResolve) ListConflictCandidates(ctx context.Context, conflictID int64) ([]*manman.WorkshopLibraryMigrationConflictCandidate, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeGCLibraryRepoForResolve) ResolveConflict(ctx context.Context, conflictID int64, resolution string, keepLibraryID *int64) error {
	return fmt.Errorf("not implemented")
}

// TestResolveGameConfigLibraryAddons_FR9_DetachReducesSetForAllDeployments is
// the FR9 regression the issue's Testing section calls out by name: with two
// deployments of one GameConfig, detaching a library at GC level must make
// the resolution helper return the reduced set for *both* deployments, with
// the single GC-level write above being the only write that happened --
// there is no per-deployment update to make this true.
func TestResolveGameConfigLibraryAddons_FR9_DetachReducesSetForAllDeployments(t *testing.T) {
	ctx := context.Background()

	const configID = int64(100)
	const libA, libB = int64(1), int64(2)
	const addonA, addonB = int64(1001), int64(1002)

	gcLibraryRepo := &fakeGCLibraryRepoForResolve{
		librariesByConfig: map[int64][]*manman.WorkshopLibrary{
			configID: {
				{LibraryID: libA},
				{LibraryID: libB},
			},
		},
	}
	libraryRepo := &fakeLibraryRepoForResolve{
		addonsByLibrary: map[int64][]*manman.WorkshopAddonWithGame{
			libA: {{WorkshopAddon: manman.WorkshopAddon{AddonID: addonA}}},
			libB: {{WorkshopAddon: manman.WorkshopAddon{AddonID: addonB}}},
		},
	}
	sgcRepo := &mockSGCRepo{sgcs: map[int64]*manman.ServerGameConfig{
		10: {SGCID: 10, GameConfigID: configID},
		20: {SGCID: 20, GameConfigID: configID},
	}}

	wm := NewWorkshopManager(nil, nil, libraryRepo, sgcRepo, nil, nil, gcLibraryRepo, nil, nil, nil, nil, nil, nil)

	for _, sgcID := range []int64{10, 20} {
		addonIDs, err := wm.ResolveGameConfigLibraryAddons(ctx, sgcID)
		if err != nil {
			t.Fatalf("ResolveGameConfigLibraryAddons(%d) before detach: %v", sgcID, err)
		}
		if _, ok := addonIDs[addonA]; !ok {
			t.Errorf("sgc %d: addon %d missing before detach", sgcID, addonA)
		}
		if _, ok := addonIDs[addonB]; !ok {
			t.Errorf("sgc %d: addon %d missing before detach", sgcID, addonB)
		}
	}

	// The single GC-level write -- exactly what
	// RemoveLibraryFromGameConfig's handler delegates to.
	if err := gcLibraryRepo.RemoveLibrary(ctx, configID, libB); err != nil {
		t.Fatalf("RemoveLibrary: %v", err)
	}

	for _, sgcID := range []int64{10, 20} {
		addonIDs, err := wm.ResolveGameConfigLibraryAddons(ctx, sgcID)
		if err != nil {
			t.Fatalf("ResolveGameConfigLibraryAddons(%d) after detach: %v", sgcID, err)
		}
		if len(addonIDs) != 1 {
			t.Fatalf("sgc %d: got %d addons after detach, want exactly 1: %v", sgcID, len(addonIDs), addonIDs)
		}
		if _, ok := addonIDs[addonA]; !ok {
			t.Errorf("sgc %d: addon %d (still attached) missing after detach", sgcID, addonA)
		}
		if _, ok := addonIDs[addonB]; ok {
			t.Errorf("sgc %d: addon %d (detached at GC level) still present", sgcID, addonB)
		}
	}
}

// TestResolveGameConfigLibraryAddons_FR8_NewDeploymentInheritsWithoutAttachStep
// is the FR8 regression: a deployment (SGC) created after a GC-level attach
// inherits the library with no attach step of its own -- the SGC below is
// never passed through AddLibraryToSGC/gcLibraryRepo.AddLibrary at all.
func TestResolveGameConfigLibraryAddons_FR8_NewDeploymentInheritsWithoutAttachStep(t *testing.T) {
	ctx := context.Background()

	const configID = int64(200)
	const libA = int64(1)
	const addonA = int64(2001)

	// Attached at GC level before any deployment of this config exists.
	gcLibraryRepo := &fakeGCLibraryRepoForResolve{
		librariesByConfig: map[int64][]*manman.WorkshopLibrary{
			configID: {{LibraryID: libA}},
		},
	}
	libraryRepo := &fakeLibraryRepoForResolve{
		addonsByLibrary: map[int64][]*manman.WorkshopAddonWithGame{
			libA: {{WorkshopAddon: manman.WorkshopAddon{AddonID: addonA}}},
		},
	}
	// A deployment created after the GC-level attach, with no per-deployment
	// attach step of its own (no AddLibrary call for sgcID 30 anywhere in
	// this test).
	sgcRepo := &mockSGCRepo{sgcs: map[int64]*manman.ServerGameConfig{
		30: {SGCID: 30, GameConfigID: configID},
	}}

	wm := NewWorkshopManager(nil, nil, libraryRepo, sgcRepo, nil, nil, gcLibraryRepo, nil, nil, nil, nil, nil, nil)

	addonIDs, err := wm.ResolveGameConfigLibraryAddons(ctx, 30)
	if err != nil {
		t.Fatalf("ResolveGameConfigLibraryAddons: %v", err)
	}
	if _, ok := addonIDs[addonA]; !ok {
		t.Errorf("new deployment did not inherit GC-level library's addon %d without its own attach step, got %v", addonA, addonIDs)
	}
}
