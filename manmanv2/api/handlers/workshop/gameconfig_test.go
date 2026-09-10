package workshop

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/whale-net/everything/manmanv2/api/repository"
	manman "github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
)

// --- fakes -----------------------------------------------------------------

// fakeGameConfigRepo is a minimal in-memory repository.GameConfigRepository
// double: only Get is exercised by the GC-level library handlers under test.
type fakeGameConfigRepo struct {
	configs map[int64]*manman.GameConfig
}

func newFakeGameConfigRepo() *fakeGameConfigRepo {
	return &fakeGameConfigRepo{configs: map[int64]*manman.GameConfig{}}
}

func (f *fakeGameConfigRepo) Get(_ context.Context, configID int64) (*manman.GameConfig, error) {
	gc, ok := f.configs[configID]
	if !ok {
		return nil, fmt.Errorf("game config %d not found", configID)
	}
	return gc, nil
}

func (f *fakeGameConfigRepo) Create(_ context.Context, _ *manman.GameConfig) (*manman.GameConfig, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeGameConfigRepo) List(_ context.Context, _ *int64, _, _ int) ([]*manman.GameConfig, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeGameConfigRepo) Update(_ context.Context, _ *manman.GameConfig) error {
	return fmt.Errorf("not implemented")
}
func (f *fakeGameConfigRepo) Delete(_ context.Context, _ int64) error {
	return fmt.Errorf("not implemented")
}

// fakeGCLibraryRepo is an in-memory repository.GameConfigWorkshopLibraryRepository
// double mirroring the real repository's documented contract (repository.go):
// AddLibrary/RemoveLibrary are idempotent and keyed on (config_id,
// library_id) with no per-deployment bookkeeping, and ResolveConflict writes
// the resulting attachment set in the same "union inserts every candidate,
// override inserts only keepLibraryID" shape the real repository's doc
// comment describes, rejecting an already-resolved conflict.
type fakeGCLibraryRepo struct {
	// attachments: configID -> libraryID -> attachment.
	attachments map[int64]map[int64]*manman.GameConfigWorkshopLibrary

	conflicts  map[int64]*manman.WorkshopLibraryMigrationConflict
	candidates map[int64][]*manman.WorkshopLibraryMigrationConflictCandidate
}

func newFakeGCLibraryRepo() *fakeGCLibraryRepo {
	return &fakeGCLibraryRepo{
		attachments: map[int64]map[int64]*manman.GameConfigWorkshopLibrary{},
		conflicts:   map[int64]*manman.WorkshopLibraryMigrationConflict{},
		candidates:  map[int64][]*manman.WorkshopLibraryMigrationConflictCandidate{},
	}
}

func (f *fakeGCLibraryRepo) ListLibraries(_ context.Context, configID int64) ([]*manman.WorkshopLibrary, error) {
	var out []*manman.WorkshopLibrary
	for libID := range f.attachments[configID] {
		out = append(out, &manman.WorkshopLibrary{LibraryID: libID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LibraryID < out[j].LibraryID })
	return out, nil
}

func (f *fakeGCLibraryRepo) ListAttachments(_ context.Context, configID int64) ([]*manman.GameConfigWorkshopLibrary, error) {
	var out []*manman.GameConfigWorkshopLibrary
	for _, a := range f.attachments[configID] {
		cp := *a
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LibraryID < out[j].LibraryID })
	return out, nil
}

func (f *fakeGCLibraryRepo) AddLibrary(_ context.Context, configID, libraryID int64, presetID, volumeID *int64, installationPathOverride *string) error {
	if f.attachments[configID] == nil {
		f.attachments[configID] = map[int64]*manman.GameConfigWorkshopLibrary{}
	}
	f.attachments[configID][libraryID] = &manman.GameConfigWorkshopLibrary{
		ConfigID:                 configID,
		LibraryID:                libraryID,
		PresetID:                 presetID,
		VolumeID:                 volumeID,
		InstallationPathOverride: installationPathOverride,
		CreatedAt:                time.Now(),
	}
	return nil
}

// RemoveLibrary is a no-op (not an error) when the library isn't attached --
// the real repository's DELETE is unconditional and affecting zero rows is
// not itself an error, matching RemoveLibraryFromGameConfig's idempotent
// contract.
func (f *fakeGCLibraryRepo) RemoveLibrary(_ context.Context, configID, libraryID int64) error {
	delete(f.attachments[configID], libraryID)
	return nil
}

func (f *fakeGCLibraryRepo) ListUnresolvedConflicts(_ context.Context) ([]*manman.WorkshopLibraryMigrationConflict, error) {
	var out []*manman.WorkshopLibraryMigrationConflict
	for _, c := range f.conflicts {
		if c.ResolvedAt == nil {
			cp := *c
			cp.Candidates = f.candidates[c.ConflictID]
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConflictID < out[j].ConflictID })
	return out, nil
}

func (f *fakeGCLibraryRepo) GetConflictForConfig(_ context.Context, configID int64) (*manman.WorkshopLibraryMigrationConflict, error) {
	for _, c := range f.conflicts {
		if c.ConfigID == configID {
			return c, nil
		}
	}
	return nil, nil
}

func (f *fakeGCLibraryRepo) ListConflictCandidates(_ context.Context, conflictID int64) ([]*manman.WorkshopLibraryMigrationConflictCandidate, error) {
	return f.candidates[conflictID], nil
}

// ResolveConflict mirrors the real repository's guarded UPDATE
// (resolved_at IS NULL): resolving a conflict that doesn't exist or is
// already resolved is rejected, which is the only source of
// ResolveLibraryMigrationConflict's FailedPrecondition in the handler.
func (f *fakeGCLibraryRepo) ResolveConflict(_ context.Context, conflictID int64, resolution string, keepLibraryID *int64) error {
	c, ok := f.conflicts[conflictID]
	if !ok || c.ResolvedAt != nil {
		return fmt.Errorf("conflict %d not found or already resolved", conflictID)
	}

	now := time.Now()
	c.ResolvedAt = &now
	res := resolution
	c.Resolution = &res
	c.ResolvedLibraryID = keepLibraryID

	if f.attachments[c.ConfigID] == nil {
		f.attachments[c.ConfigID] = map[int64]*manman.GameConfigWorkshopLibrary{}
	}
	if resolution == "override" {
		f.attachments[c.ConfigID][*keepLibraryID] = &manman.GameConfigWorkshopLibrary{
			ConfigID: c.ConfigID, LibraryID: *keepLibraryID, CreatedAt: now,
		}
		return nil
	}
	// union: attach every candidate library.
	for _, cand := range f.candidates[conflictID] {
		f.attachments[c.ConfigID][cand.LibraryID] = &manman.GameConfigWorkshopLibrary{
			ConfigID: c.ConfigID, LibraryID: cand.LibraryID, CreatedAt: now,
		}
	}
	return nil
}

// --- test helpers ------------------------------------------------------------

func newGameConfigTestHandler(gameConfigRepo repository.GameConfigRepository, libraryRepo repository.WorkshopLibraryRepository, gcLibraryRepo repository.GameConfigWorkshopLibraryRepository) *WorkshopServiceHandler {
	return &WorkshopServiceHandler{
		gameConfigRepo: gameConfigRepo,
		libraryRepo:    libraryRepo,
		gcLibraryRepo:  gcLibraryRepo,
	}
}

// --- AddLibraryToGameConfig / ListGameConfigLibraries (FR8) ------------------

func TestAddLibraryToGameConfig_AttachThenListReturnsIt(t *testing.T) {
	gcRepo := newFakeGameConfigRepo()
	gcRepo.configs[1] = &manman.GameConfig{ConfigID: 1, GameID: 5}
	libRepo := new(MockWorkshopLibraryRepository)
	libRepo.On("Get", mock.Anything, int64(10)).Return(&manman.WorkshopLibrary{LibraryID: 10, GameID: 5}, nil)
	gcLibRepo := newFakeGCLibraryRepo()

	h := newGameConfigTestHandler(gcRepo, libRepo, gcLibRepo)

	_, err := h.AddLibraryToGameConfig(context.Background(), &pb.AddLibraryToGameConfigRequest{ConfigId: 1, LibraryId: 10})
	if err != nil {
		t.Fatalf("AddLibraryToGameConfig: unexpected error: %v", err)
	}

	resp, err := h.ListGameConfigLibraries(context.Background(), &pb.ListGameConfigLibrariesRequest{ConfigId: 1})
	if err != nil {
		t.Fatalf("ListGameConfigLibraries: unexpected error: %v", err)
	}
	if len(resp.Libraries) != 1 || resp.Libraries[0].LibraryId != 10 {
		t.Fatalf("ListGameConfigLibraries = %+v, want exactly library 10", resp.Libraries)
	}
}

// TestAddLibraryToGameConfig_ReAttachIsIdempotent proves a re-attach of the
// same (config_id, library_id) doesn't error or duplicate the list entry --
// it just updates the override columns, per the handler's doc comment.
func TestAddLibraryToGameConfig_ReAttachIsIdempotent(t *testing.T) {
	gcRepo := newFakeGameConfigRepo()
	gcRepo.configs[1] = &manman.GameConfig{ConfigID: 1, GameID: 5}
	libRepo := new(MockWorkshopLibraryRepository)
	libRepo.On("Get", mock.Anything, int64(10)).Return(&manman.WorkshopLibrary{LibraryID: 10, GameID: 5}, nil)
	gcLibRepo := newFakeGCLibraryRepo()
	h := newGameConfigTestHandler(gcRepo, libRepo, gcLibRepo)

	for i := 0; i < 2; i++ {
		if _, err := h.AddLibraryToGameConfig(context.Background(), &pb.AddLibraryToGameConfigRequest{ConfigId: 1, LibraryId: 10}); err != nil {
			t.Fatalf("AddLibraryToGameConfig call %d: unexpected error: %v", i, err)
		}
	}

	resp, err := h.ListGameConfigLibraries(context.Background(), &pb.ListGameConfigLibrariesRequest{ConfigId: 1})
	if err != nil {
		t.Fatalf("ListGameConfigLibraries: unexpected error: %v", err)
	}
	if len(resp.Libraries) != 1 {
		t.Fatalf("ListGameConfigLibraries returned %d libraries after a re-attach, want exactly 1 (idempotent)", len(resp.Libraries))
	}
}

func TestAddLibraryToGameConfig_GameMismatch_InvalidArgument(t *testing.T) {
	gcRepo := newFakeGameConfigRepo()
	gcRepo.configs[1] = &manman.GameConfig{ConfigID: 1, GameID: 5}
	libRepo := new(MockWorkshopLibraryRepository)
	libRepo.On("Get", mock.Anything, int64(10)).Return(&manman.WorkshopLibrary{LibraryID: 10, GameID: 999}, nil)
	gcLibRepo := newFakeGCLibraryRepo()
	h := newGameConfigTestHandler(gcRepo, libRepo, gcLibRepo)

	_, err := h.AddLibraryToGameConfig(context.Background(), &pb.AddLibraryToGameConfigRequest{ConfigId: 1, LibraryId: 10})
	assertCode(t, err, codes.InvalidArgument)

	resp, listErr := h.ListGameConfigLibraries(context.Background(), &pb.ListGameConfigLibrariesRequest{ConfigId: 1})
	if listErr != nil {
		t.Fatalf("ListGameConfigLibraries: unexpected error: %v", listErr)
	}
	if len(resp.Libraries) != 0 {
		t.Errorf("a rejected attach must not leave a partial attachment, got %+v", resp.Libraries)
	}
}

func TestAddLibraryToGameConfig_ConfigNotFound_InvalidArgument(t *testing.T) {
	gcRepo := newFakeGameConfigRepo()
	libRepo := new(MockWorkshopLibraryRepository)
	gcLibRepo := newFakeGCLibraryRepo()
	h := newGameConfigTestHandler(gcRepo, libRepo, gcLibRepo)

	_, err := h.AddLibraryToGameConfig(context.Background(), &pb.AddLibraryToGameConfigRequest{ConfigId: 999, LibraryId: 10})
	assertCode(t, err, codes.InvalidArgument)
}

func TestAddLibraryToGameConfig_LibraryNotFound_InvalidArgument(t *testing.T) {
	gcRepo := newFakeGameConfigRepo()
	gcRepo.configs[1] = &manman.GameConfig{ConfigID: 1, GameID: 5}
	libRepo := new(MockWorkshopLibraryRepository)
	libRepo.On("Get", mock.Anything, int64(999)).Return(nil, fmt.Errorf("not found"))
	gcLibRepo := newFakeGCLibraryRepo()
	h := newGameConfigTestHandler(gcRepo, libRepo, gcLibRepo)

	_, err := h.AddLibraryToGameConfig(context.Background(), &pb.AddLibraryToGameConfigRequest{ConfigId: 1, LibraryId: 999})
	assertCode(t, err, codes.InvalidArgument)
}

func TestAddLibraryToGameConfig_MissingIDs_InvalidArgument(t *testing.T) {
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), newFakeGCLibraryRepo())

	_, err := h.AddLibraryToGameConfig(context.Background(), &pb.AddLibraryToGameConfigRequest{LibraryId: 10})
	assertCode(t, err, codes.InvalidArgument)

	_, err = h.AddLibraryToGameConfig(context.Background(), &pb.AddLibraryToGameConfigRequest{ConfigId: 1})
	assertCode(t, err, codes.InvalidArgument)
}

// --- RemoveLibraryFromGameConfig (FR9) ---------------------------------------

func TestRemoveLibraryFromGameConfig_DetachRemovesFromList(t *testing.T) {
	gcLibRepo := newFakeGCLibraryRepo()
	gcLibRepo.attachments[1] = map[int64]*manman.GameConfigWorkshopLibrary{
		10: {ConfigID: 1, LibraryID: 10},
	}
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), gcLibRepo)

	if _, err := h.RemoveLibraryFromGameConfig(context.Background(), &pb.RemoveLibraryFromGameConfigRequest{ConfigId: 1, LibraryId: 10}); err != nil {
		t.Fatalf("RemoveLibraryFromGameConfig: unexpected error: %v", err)
	}

	resp, err := h.ListGameConfigLibraries(context.Background(), &pb.ListGameConfigLibrariesRequest{ConfigId: 1})
	if err != nil {
		t.Fatalf("ListGameConfigLibraries: unexpected error: %v", err)
	}
	if len(resp.Libraries) != 0 {
		t.Errorf("ListGameConfigLibraries = %+v after detach, want empty", resp.Libraries)
	}
}

// TestRemoveLibraryFromGameConfig_UnattachedIsIdempotentSuccess pins the
// handler's documented contract: removing a library that was never attached
// is a success, not an error.
func TestRemoveLibraryFromGameConfig_UnattachedIsIdempotentSuccess(t *testing.T) {
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), newFakeGCLibraryRepo())

	if _, err := h.RemoveLibraryFromGameConfig(context.Background(), &pb.RemoveLibraryFromGameConfigRequest{ConfigId: 1, LibraryId: 10}); err != nil {
		t.Fatalf("RemoveLibraryFromGameConfig on an unattached library: unexpected error: %v", err)
	}
}

func TestRemoveLibraryFromGameConfig_MissingIDs_InvalidArgument(t *testing.T) {
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), newFakeGCLibraryRepo())

	_, err := h.RemoveLibraryFromGameConfig(context.Background(), &pb.RemoveLibraryFromGameConfigRequest{LibraryId: 10})
	assertCode(t, err, codes.InvalidArgument)

	_, err = h.RemoveLibraryFromGameConfig(context.Background(), &pb.RemoveLibraryFromGameConfigRequest{ConfigId: 1})
	assertCode(t, err, codes.InvalidArgument)
}

// --- ListGameConfigLibraries / GetGameConfigLibraryAttachments ---------------

func TestListGameConfigLibraries_MissingConfigID_InvalidArgument(t *testing.T) {
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), newFakeGCLibraryRepo())

	_, err := h.ListGameConfigLibraries(context.Background(), &pb.ListGameConfigLibrariesRequest{})
	assertCode(t, err, codes.InvalidArgument)
}

// TestGetGameConfigLibraryAttachments_ReturnsOverrides proves the override
// columns (preset_id, volume_id, installation_path_override) round-trip
// through GetGameConfigLibraryAttachments identically in shape to the SGC
// form, per the issue's scaffold note.
func TestGetGameConfigLibraryAttachments_ReturnsOverrides(t *testing.T) {
	presetID := int64(7)
	volumeID := int64(3)
	pathOverride := "/custom/path"

	gcRepo := newFakeGameConfigRepo()
	gcRepo.configs[1] = &manman.GameConfig{ConfigID: 1, GameID: 5}
	libRepo := new(MockWorkshopLibraryRepository)
	libRepo.On("Get", mock.Anything, int64(10)).Return(&manman.WorkshopLibrary{LibraryID: 10, GameID: 5}, nil)
	gcLibRepo := newFakeGCLibraryRepo()
	h := newGameConfigTestHandler(gcRepo, libRepo, gcLibRepo)

	_, err := h.AddLibraryToGameConfig(context.Background(), &pb.AddLibraryToGameConfigRequest{
		ConfigId: 1, LibraryId: 10,
		PresetId: presetID, VolumeId: volumeID, InstallationPathOverride: pathOverride,
	})
	if err != nil {
		t.Fatalf("AddLibraryToGameConfig: unexpected error: %v", err)
	}

	resp, err := h.GetGameConfigLibraryAttachments(context.Background(), &pb.GetGameConfigLibraryAttachmentsRequest{ConfigId: 1})
	if err != nil {
		t.Fatalf("GetGameConfigLibraryAttachments: unexpected error: %v", err)
	}
	if len(resp.Attachments) != 1 {
		t.Fatalf("Attachments = %+v, want exactly 1", resp.Attachments)
	}
	a := resp.Attachments[0]
	if a.PresetId != presetID || a.VolumeId != volumeID || a.InstallationPathOverride != pathOverride {
		t.Errorf("Attachment = %+v, want preset=%d volume=%d path=%q", a, presetID, volumeID, pathOverride)
	}
}

// --- ListLibraryMigrationConflicts / ResolveLibraryMigrationConflict (FR12) --

func seedConflict(repo *fakeGCLibraryRepo, conflictID, configID int64, candidateLibraryIDs ...int64) {
	repo.conflicts[conflictID] = &manman.WorkshopLibraryMigrationConflict{
		ConflictID: conflictID,
		ConfigID:   configID,
		DetectedAt: time.Now(),
	}
	var candidates []*manman.WorkshopLibraryMigrationConflictCandidate
	for i, libID := range candidateLibraryIDs {
		candidates = append(candidates, &manman.WorkshopLibraryMigrationConflictCandidate{
			ConflictID: conflictID, LibraryID: libID, SGCID: int64(100 + i),
		})
	}
	repo.candidates[conflictID] = candidates
}

func TestListLibraryMigrationConflicts_OnlyUnresolved(t *testing.T) {
	gcLibRepo := newFakeGCLibraryRepo()
	seedConflict(gcLibRepo, 1, 10, 20, 21)
	seedConflict(gcLibRepo, 2, 11, 30, 31)
	// Resolve conflict 2 so it must be excluded from the unresolved list.
	if err := gcLibRepo.ResolveConflict(context.Background(), 2, "union", nil); err != nil {
		t.Fatalf("seed ResolveConflict: %v", err)
	}

	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), gcLibRepo)
	resp, err := h.ListLibraryMigrationConflicts(context.Background(), &pb.ListLibraryMigrationConflictsRequest{})
	if err != nil {
		t.Fatalf("ListLibraryMigrationConflicts: unexpected error: %v", err)
	}
	if len(resp.Conflicts) != 1 || resp.Conflicts[0].ConflictId != 1 {
		t.Fatalf("Conflicts = %+v, want exactly unresolved conflict 1", resp.Conflicts)
	}
	if len(resp.Conflicts[0].Candidates) != 2 {
		t.Errorf("Conflict 1 candidates = %+v, want 2", resp.Conflicts[0].Candidates)
	}
}

func TestResolveLibraryMigrationConflict_UnionAttachesAllCandidates(t *testing.T) {
	gcLibRepo := newFakeGCLibraryRepo()
	seedConflict(gcLibRepo, 1, 10, 20, 21)
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), gcLibRepo)

	_, err := h.ResolveLibraryMigrationConflict(context.Background(), &pb.ResolveLibraryMigrationConflictRequest{ConflictId: 1, Resolution: "union"})
	if err != nil {
		t.Fatalf("ResolveLibraryMigrationConflict: unexpected error: %v", err)
	}

	resp, err := h.ListGameConfigLibraries(context.Background(), &pb.ListGameConfigLibrariesRequest{ConfigId: 10})
	if err != nil {
		t.Fatalf("ListGameConfigLibraries: unexpected error: %v", err)
	}
	if len(resp.Libraries) != 2 {
		t.Fatalf("ListGameConfigLibraries = %+v after union resolution, want both candidates attached", resp.Libraries)
	}
}

func TestResolveLibraryMigrationConflict_OverrideAttachesOnlyKeptLibrary(t *testing.T) {
	gcLibRepo := newFakeGCLibraryRepo()
	seedConflict(gcLibRepo, 1, 10, 20, 21)
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), gcLibRepo)

	_, err := h.ResolveLibraryMigrationConflict(context.Background(), &pb.ResolveLibraryMigrationConflictRequest{
		ConflictId: 1, Resolution: "override", KeepLibraryId: 21,
	})
	if err != nil {
		t.Fatalf("ResolveLibraryMigrationConflict: unexpected error: %v", err)
	}

	resp, err := h.ListGameConfigLibraries(context.Background(), &pb.ListGameConfigLibrariesRequest{ConfigId: 10})
	if err != nil {
		t.Fatalf("ListGameConfigLibraries: unexpected error: %v", err)
	}
	if len(resp.Libraries) != 1 || resp.Libraries[0].LibraryId != 21 {
		t.Fatalf("ListGameConfigLibraries = %+v, want exactly the kept library 21", resp.Libraries)
	}
}

func TestResolveLibraryMigrationConflict_OverrideWithoutKeepLibraryID_InvalidArgument(t *testing.T) {
	gcLibRepo := newFakeGCLibraryRepo()
	seedConflict(gcLibRepo, 1, 10, 20, 21)
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), gcLibRepo)

	_, err := h.ResolveLibraryMigrationConflict(context.Background(), &pb.ResolveLibraryMigrationConflictRequest{ConflictId: 1, Resolution: "override"})
	assertCode(t, err, codes.InvalidArgument)
}

func TestResolveLibraryMigrationConflict_OverrideWithBadKeepLibraryID_InvalidArgument(t *testing.T) {
	gcLibRepo := newFakeGCLibraryRepo()
	seedConflict(gcLibRepo, 1, 10, 20, 21)
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), gcLibRepo)

	_, err := h.ResolveLibraryMigrationConflict(context.Background(), &pb.ResolveLibraryMigrationConflictRequest{
		ConflictId: 1, Resolution: "override", KeepLibraryId: 999,
	})
	assertCode(t, err, codes.InvalidArgument)
}

// TestResolveLibraryMigrationConflict_InvalidResolutionShape_InvalidArgument
// pins the FR12 "no other resolution shape" out-of-scope constraint: any
// resolution string besides "union"/"override" is rejected.
func TestResolveLibraryMigrationConflict_InvalidResolutionShape_InvalidArgument(t *testing.T) {
	gcLibRepo := newFakeGCLibraryRepo()
	seedConflict(gcLibRepo, 1, 10, 20, 21)
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), gcLibRepo)

	_, err := h.ResolveLibraryMigrationConflict(context.Background(), &pb.ResolveLibraryMigrationConflictRequest{ConflictId: 1, Resolution: "merge"})
	assertCode(t, err, codes.InvalidArgument)
}

func TestResolveLibraryMigrationConflict_AlreadyResolved_FailedPrecondition(t *testing.T) {
	gcLibRepo := newFakeGCLibraryRepo()
	seedConflict(gcLibRepo, 1, 10, 20, 21)
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), gcLibRepo)

	if _, err := h.ResolveLibraryMigrationConflict(context.Background(), &pb.ResolveLibraryMigrationConflictRequest{ConflictId: 1, Resolution: "union"}); err != nil {
		t.Fatalf("first resolve: unexpected error: %v", err)
	}

	_, err := h.ResolveLibraryMigrationConflict(context.Background(), &pb.ResolveLibraryMigrationConflictRequest{ConflictId: 1, Resolution: "union"})
	assertCode(t, err, codes.FailedPrecondition)
}

func TestResolveLibraryMigrationConflict_MissingConflictID_InvalidArgument(t *testing.T) {
	h := newGameConfigTestHandler(newFakeGameConfigRepo(), new(MockWorkshopLibraryRepository), newFakeGCLibraryRepo())

	_, err := h.ResolveLibraryMigrationConflict(context.Background(), &pb.ResolveLibraryMigrationConflictRequest{Resolution: "union"})
	assertCode(t, err, codes.InvalidArgument)
}
