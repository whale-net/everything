package workshop

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MockWorkshopAddonRepository is a mock implementation of WorkshopAddonRepository
type MockWorkshopAddonRepository struct {
	mock.Mock
}

func (m *MockWorkshopAddonRepository) Create(ctx context.Context, addon *manman.WorkshopAddon) (*manman.WorkshopAddon, error) {
	args := m.Called(ctx, addon)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopAddon), args.Error(1)
}

func (m *MockWorkshopAddonRepository) Get(ctx context.Context, addonID int64) (*manman.WorkshopAddon, error) {
	args := m.Called(ctx, addonID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopAddon), args.Error(1)
}

func (m *MockWorkshopAddonRepository) List(ctx context.Context, gameID *int64, includeDeprecated bool, limit, offset int) ([]*manman.WorkshopAddon, error) {
	args := m.Called(ctx, gameID, includeDeprecated, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.WorkshopAddon), args.Error(1)
}

func (m *MockWorkshopAddonRepository) ListByCollectionID(ctx context.Context, collectionID int64) ([]*manman.WorkshopAddon, error) {
	args := m.Called(ctx, collectionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.WorkshopAddon), args.Error(1)
}

func (m *MockWorkshopAddonRepository) Update(ctx context.Context, addon *manman.WorkshopAddon) error {
	args := m.Called(ctx, addon)
	return args.Error(0)
}

func (m *MockWorkshopAddonRepository) GetByWorkshopID(ctx context.Context, gameID int64, workshopID string, platformType string) (*manman.WorkshopAddon, error) {
	args := m.Called(ctx, gameID, workshopID, platformType)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopAddon), args.Error(1)
}

func (m *MockWorkshopAddonRepository) Delete(ctx context.Context, addonID int64) error {
	args := m.Called(ctx, addonID)
	return args.Error(0)
}

// GetByWorkshopIDAnyGame backs #2186's on-demand verify RPC (see
// repository.go's WorkshopAddonRepository doc comment). Tests that don't
// exercise VerifyCacheEntry never call .On(...) for it, so it's never
// invoked -- only present so MockWorkshopAddonRepository keeps satisfying
// the interface.
func (m *MockWorkshopAddonRepository) GetByWorkshopIDAnyGame(ctx context.Context, workshopID string) (*manman.WorkshopAddonWithGame, error) {
	args := m.Called(ctx, workshopID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopAddonWithGame), args.Error(1)
}

// MockWorkshopInstallationRepository is a mock implementation of WorkshopInstallationRepository
type MockWorkshopInstallationRepository struct {
	mock.Mock
}

func (m *MockWorkshopInstallationRepository) Create(ctx context.Context, installation *manman.WorkshopInstallation) (*manman.WorkshopInstallation, error) {
	args := m.Called(ctx, installation)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopInstallation), args.Error(1)
}

func (m *MockWorkshopInstallationRepository) Get(ctx context.Context, installationID int64) (*manman.WorkshopInstallation, error) {
	args := m.Called(ctx, installationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopInstallation), args.Error(1)
}

func (m *MockWorkshopInstallationRepository) GetBySGCAndAddon(ctx context.Context, sgcID, addonID int64) (*manman.WorkshopInstallation, error) {
	args := m.Called(ctx, sgcID, addonID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopInstallation), args.Error(1)
}

func (m *MockWorkshopInstallationRepository) List(ctx context.Context, limit, offset int) ([]*manman.WorkshopInstallation, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.WorkshopInstallation), args.Error(1)
}

func (m *MockWorkshopInstallationRepository) ListBySGC(ctx context.Context, sgcID int64, limit, offset int) ([]*manman.WorkshopInstallation, error) {
	args := m.Called(ctx, sgcID, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.WorkshopInstallation), args.Error(1)
}

func (m *MockWorkshopInstallationRepository) ListByAddon(ctx context.Context, addonID int64, limit, offset int) ([]*manman.WorkshopInstallation, error) {
	args := m.Called(ctx, addonID, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.WorkshopInstallation), args.Error(1)
}

func (m *MockWorkshopInstallationRepository) UpdateStatus(ctx context.Context, installationID int64, status string, errorMsg *string) error {
	args := m.Called(ctx, installationID, status, errorMsg)
	return args.Error(0)
}

func (m *MockWorkshopInstallationRepository) UpdateProgress(ctx context.Context, installationID int64, percent int) error {
	args := m.Called(ctx, installationID, percent)
	return args.Error(0)
}

func (m *MockWorkshopInstallationRepository) Delete(ctx context.Context, installationID int64) error {
	args := m.Called(ctx, installationID)
	return args.Error(0)
}

// MockWorkshopLibraryRepository is a mock implementation of WorkshopLibraryRepository
type MockWorkshopLibraryRepository struct {
	mock.Mock
}

func (m *MockWorkshopLibraryRepository) Create(ctx context.Context, library *manman.WorkshopLibrary) (*manman.WorkshopLibrary, error) {
	args := m.Called(ctx, library)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopLibrary), args.Error(1)
}

func (m *MockWorkshopLibraryRepository) Get(ctx context.Context, libraryID int64) (*manman.WorkshopLibrary, error) {
	args := m.Called(ctx, libraryID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopLibrary), args.Error(1)
}

func (m *MockWorkshopLibraryRepository) List(ctx context.Context, gameID *int64, limit, offset int) ([]*manman.WorkshopLibrary, error) {
	args := m.Called(ctx, gameID, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.WorkshopLibrary), args.Error(1)
}

func (m *MockWorkshopLibraryRepository) Update(ctx context.Context, library *manman.WorkshopLibrary) error {
	args := m.Called(ctx, library)
	return args.Error(0)
}

func (m *MockWorkshopLibraryRepository) Delete(ctx context.Context, libraryID int64) error {
	args := m.Called(ctx, libraryID)
	return args.Error(0)
}

func (m *MockWorkshopLibraryRepository) AddAddon(ctx context.Context, libraryID, addonID int64, displayOrder int) error {
	args := m.Called(ctx, libraryID, addonID, displayOrder)
	return args.Error(0)
}

func (m *MockWorkshopLibraryRepository) RemoveAddon(ctx context.Context, libraryID, addonID int64) error {
	args := m.Called(ctx, libraryID, addonID)
	return args.Error(0)
}

func (m *MockWorkshopLibraryRepository) ListAddons(ctx context.Context, libraryID int64) ([]*manman.WorkshopAddonWithGame, error) {
	args := m.Called(ctx, libraryID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.WorkshopAddonWithGame), args.Error(1)
}

func (m *MockWorkshopLibraryRepository) AddReference(ctx context.Context, parentLibraryID, childLibraryID int64) error {
	args := m.Called(ctx, parentLibraryID, childLibraryID)
	return args.Error(0)
}

func (m *MockWorkshopLibraryRepository) RemoveReference(ctx context.Context, parentLibraryID, childLibraryID int64) error {
	args := m.Called(ctx, parentLibraryID, childLibraryID)
	return args.Error(0)
}

func (m *MockWorkshopLibraryRepository) ListReferences(ctx context.Context, libraryID int64) ([]*manman.WorkshopLibrary, error) {
	args := m.Called(ctx, libraryID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.WorkshopLibrary), args.Error(1)
}

func (m *MockWorkshopLibraryRepository) DetectCircularReference(ctx context.Context, parentLibraryID, childLibraryID int64) (bool, error) {
	args := m.Called(ctx, parentLibraryID, childLibraryID)
	return args.Bool(0), args.Error(1)
}

// MockWorkshopBatchJobRepository is a mock implementation of
// WorkshopBatchJobRepository (#2179, plan #2175, FR4).
type MockWorkshopBatchJobRepository struct {
	mock.Mock
}

func (m *MockWorkshopBatchJobRepository) CreateBatchJob(ctx context.Context, job *manman.WorkshopBatchJob) (*manman.WorkshopBatchJob, error) {
	args := m.Called(ctx, job)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopBatchJob), args.Error(1)
}

func (m *MockWorkshopBatchJobRepository) CreateBatchJobItems(ctx context.Context, batchJobID int64, items []*manman.WorkshopBatchJobItem) error {
	args := m.Called(ctx, batchJobID, items)
	return args.Error(0)
}

func (m *MockWorkshopBatchJobRepository) UpdateBatchJobItemResult(ctx context.Context, batchJobItemID int64, status string, addonID *int64, errorMessage *string) error {
	args := m.Called(ctx, batchJobItemID, status, addonID, errorMessage)
	return args.Error(0)
}

func (m *MockWorkshopBatchJobRepository) UpdateBatchJobStatus(ctx context.Context, batchJobID int64, status string, succeeded, failed int) error {
	args := m.Called(ctx, batchJobID, status, succeeded, failed)
	return args.Error(0)
}

func (m *MockWorkshopBatchJobRepository) GetBatchJob(ctx context.Context, batchJobID int64) (*manman.WorkshopBatchJob, error) {
	args := m.Called(ctx, batchJobID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopBatchJob), args.Error(1)
}

func (m *MockWorkshopBatchJobRepository) ListBatchJobItems(ctx context.Context, batchJobID int64) ([]*manman.WorkshopBatchJobItem, error) {
	args := m.Called(ctx, batchJobID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.WorkshopBatchJobItem), args.Error(1)
}

func (m *MockWorkshopBatchJobRepository) ListBatchJobs(ctx context.Context, gameID int64, limit int) ([]*manman.WorkshopBatchJob, error) {
	args := m.Called(ctx, gameID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.WorkshopBatchJob), args.Error(1)
}

// MockAddonPathPresetRepository is a mock implementation of AddonPathPresetRepository
type MockAddonPathPresetRepository struct {
	mock.Mock
}

func (m *MockAddonPathPresetRepository) Create(ctx context.Context, preset *manman.GameAddonPathPreset) (*manman.GameAddonPathPreset, error) {
	args := m.Called(ctx, preset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.GameAddonPathPreset), args.Error(1)
}

func (m *MockAddonPathPresetRepository) Get(ctx context.Context, presetID int64) (*manman.GameAddonPathPreset, error) {
	args := m.Called(ctx, presetID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.GameAddonPathPreset), args.Error(1)
}

func (m *MockAddonPathPresetRepository) ListByGame(ctx context.Context, gameID int64) ([]*manman.GameAddonPathPreset, error) {
	args := m.Called(ctx, gameID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.GameAddonPathPreset), args.Error(1)
}

func (m *MockAddonPathPresetRepository) Update(ctx context.Context, preset *manman.GameAddonPathPreset) error {
	args := m.Called(ctx, preset)
	return args.Error(0)
}

func (m *MockAddonPathPresetRepository) Delete(ctx context.Context, presetID int64) error {
	args := m.Called(ctx, presetID)
	return args.Error(0)
}

// MockWorkshopManager is a mock implementation of WorkshopManager
type MockWorkshopManager struct {
	mock.Mock
}

func (m *MockWorkshopManager) InstallAddon(ctx context.Context, sgcID, addonID int64, forceReinstall, skipDispatch bool, installationPathOverride string, presetIDOverride int64, volumeIDOverride int64) (*manman.WorkshopInstallation, error) {
	args := m.Called(ctx, sgcID, addonID, forceReinstall, skipDispatch, installationPathOverride, presetIDOverride, volumeIDOverride)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopInstallation), args.Error(1)
}

func (m *MockWorkshopManager) RemoveInstallation(ctx context.Context, installationID int64) error {
	args := m.Called(ctx, installationID)
	return args.Error(0)
}

func (m *MockWorkshopManager) ResetInstallation(ctx context.Context, installationID int64) (*manman.WorkshopInstallation, error) {
	args := m.Called(ctx, installationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopInstallation), args.Error(1)
}

func (m *MockWorkshopManager) FetchMetadata(ctx context.Context, gameID int64, workshopID string) (*manman.WorkshopAddon, error) {
	args := m.Called(ctx, gameID, workshopID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopAddon), args.Error(1)
}

func (m *MockWorkshopManager) CreateAddon(ctx context.Context, addon *manman.WorkshopAddon) (*manman.WorkshopAddon, error) {
	args := m.Called(ctx, addon)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopAddon), args.Error(1)
}

func (m *MockWorkshopManager) EnsureLibraryAddonsInstalled(ctx context.Context, sgcID int64) error {
	args := m.Called(ctx, sgcID)
	return args.Error(0)
}

func (m *MockWorkshopManager) BatchCreateAddons(ctx context.Context, gameID, libraryID int64, entries string, presetID int64) (*manman.WorkshopBatchJob, []*manman.WorkshopBatchJobItem, error) {
	args := m.Called(ctx, gameID, libraryID, entries, presetID)
	var job *manman.WorkshopBatchJob
	if args.Get(0) != nil {
		job = args.Get(0).(*manman.WorkshopBatchJob)
	}
	var items []*manman.WorkshopBatchJobItem
	if args.Get(1) != nil {
		items = args.Get(1).([]*manman.WorkshopBatchJobItem)
	}
	return job, items, args.Error(2)
}

func (m *MockWorkshopManager) AddCollectionToLibrary(ctx context.Context, gameID, libraryID int64, collectionInput string, presetID int64) (*manman.WorkshopBatchJob, int64, []*manman.WorkshopBatchJobItem, error) {
	args := m.Called(ctx, gameID, libraryID, collectionInput, presetID)
	var job *manman.WorkshopBatchJob
	if args.Get(0) != nil {
		job = args.Get(0).(*manman.WorkshopBatchJob)
	}
	var items []*manman.WorkshopBatchJobItem
	if args.Get(2) != nil {
		items = args.Get(2).([]*manman.WorkshopBatchJobItem)
	}
	return job, args.Get(1).(int64), items, args.Error(3)
}

// TestAddCollectionToLibrary tests the AddCollectionToLibrary RPC's request
// validation and its delegation to WorkshopManager.AddCollectionToLibrary
// (FR1, FR3): the handler validates game_id/library_id/preset_id and
// otherwise treats partial per-item failure within the collection as a
// normal (non-error) response, while a job-level error from the manager
// (e.g. an unresolvable collection) surfaces as codes.Internal.
func TestAddCollectionToLibrary(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.AddCollectionToLibraryRequest
		mockSetup     func(*MockWorkshopManager, *MockWorkshopLibraryRepository, *MockAddonPathPresetRepository)
		expectedError codes.Code
		checkResponse func(*testing.T, *pb.AddCollectionToLibraryResponse)
	}{
		{
			name:          "missing game_id",
			request:       &pb.AddCollectionToLibraryRequest{CollectionInput: "999999"},
			mockSetup:     func(*MockWorkshopManager, *MockWorkshopLibraryRepository, *MockAddonPathPresetRepository) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name:          "missing collection_input",
			request:       &pb.AddCollectionToLibraryRequest{GameId: 1},
			mockSetup:     func(*MockWorkshopManager, *MockWorkshopLibraryRepository, *MockAddonPathPresetRepository) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name:    "library not found",
			request: &pb.AddCollectionToLibraryRequest{GameId: 1, CollectionInput: "999999", LibraryId: 5},
			mockSetup: func(m *MockWorkshopManager, l *MockWorkshopLibraryRepository, p *MockAddonPathPresetRepository) {
				l.On("Get", mock.Anything, int64(5)).Return(nil, assert.AnError)
			},
			expectedError: codes.NotFound,
		},
		{
			name:    "preset not found",
			request: &pb.AddCollectionToLibraryRequest{GameId: 1, CollectionInput: "999999", PresetId: 9},
			mockSetup: func(m *MockWorkshopManager, l *MockWorkshopLibraryRepository, p *MockAddonPathPresetRepository) {
				p.On("Get", mock.Anything, int64(9)).Return(nil, assert.AnError)
			},
			expectedError: codes.NotFound,
		},
		{
			name:    "manager-level error surfaces as Internal (e.g. unresolvable collection)",
			request: &pb.AddCollectionToLibraryRequest{GameId: 1, CollectionInput: "000000"},
			mockSetup: func(m *MockWorkshopManager, l *MockWorkshopLibraryRepository, p *MockAddonPathPresetRepository) {
				m.On("AddCollectionToLibrary", mock.Anything, int64(1), int64(0), "000000", int64(0)).
					Return(nil, int64(0), nil, assert.AnError)
			},
			expectedError: codes.Internal,
		},
		{
			name:    "successful collection add with mixed per-item results",
			request: &pb.AddCollectionToLibraryRequest{GameId: 1, CollectionInput: "999999", LibraryId: 5},
			mockSetup: func(m *MockWorkshopManager, l *MockWorkshopLibraryRepository, p *MockAddonPathPresetRepository) {
				l.On("Get", mock.Anything, int64(5)).Return(&manman.WorkshopLibrary{LibraryID: 5}, nil)
				addonID := int64(10)
				workshopID := "111111"
				errMsg := "workshop item 404404: workshop item not found"
				m.On("AddCollectionToLibrary", mock.Anything, int64(1), int64(5), "999999", int64(0)).
					Return(&manman.WorkshopBatchJob{
						BatchJobID:     2,
						TotalItems:     2,
						SucceededItems: 1,
						FailedItems:    1,
						Status:         "completed_with_errors",
					}, int64(99), []*manman.WorkshopBatchJobItem{
						{RawInput: "111111", WorkshopID: &workshopID, AddonID: &addonID, Status: "succeeded"},
						{RawInput: "404404", Status: "failed", ErrorMessage: &errMsg},
					}, nil)
			},
			expectedError: codes.OK,
			checkResponse: func(t *testing.T, resp *pb.AddCollectionToLibraryResponse) {
				assert.Equal(t, int64(2), resp.BatchJobId)
				assert.Equal(t, int64(99), resp.CollectionAddonId)
				assert.Equal(t, int32(2), resp.TotalItems)
				assert.Equal(t, int32(1), resp.SucceededItems)
				assert.Equal(t, int32(1), resp.FailedItems)
				if assert.Len(t, resp.Results, 2) {
					assert.Equal(t, "succeeded", resp.Results[0].Status)
					assert.Equal(t, "111111", resp.Results[0].WorkshopId)
					assert.Equal(t, int64(10), resp.Results[0].AddonId)
					assert.Equal(t, "failed", resp.Results[1].Status)
					assert.NotEmpty(t, resp.Results[1].ErrorMessage)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockManager := new(MockWorkshopManager)
			mockLibraryRepo := new(MockWorkshopLibraryRepository)
			mockPresetRepo := new(MockAddonPathPresetRepository)
			tt.mockSetup(mockManager, mockLibraryRepo, mockPresetRepo)

			handler := &WorkshopServiceHandler{
				workshopManager: mockManager,
				libraryRepo:     mockLibraryRepo,
				presetRepo:      mockPresetRepo,
			}

			resp, err := handler.AddCollectionToLibrary(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				if tt.checkResponse != nil {
					tt.checkResponse(t, resp)
				}
			}

			mockManager.AssertExpectations(t)
			mockLibraryRepo.AssertExpectations(t)
			mockPresetRepo.AssertExpectations(t)
		})
	}
}

// TestInstallAddon tests the InstallAddon RPC
func TestInstallAddon(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.InstallAddonRequest
		mockSetup     func(*MockWorkshopManager)
		expectedError codes.Code
	}{
		{
			name: "successful installation",
			request: &pb.InstallAddonRequest{
				SgcId:   1,
				AddonId: 100,
			},
			mockSetup: func(m *MockWorkshopManager) {
				m.On("InstallAddon", mock.Anything, int64(1), int64(100), false, false, "", int64(0), int64(0)).
					Return(&manman.WorkshopInstallation{
						InstallationID:   1,
						SGCID:            1,
						AddonID:          100,
						Status:           manman.InstallationStatusPending,
						InstallationPath: "/path/to/addon",
					}, nil)
			},
			expectedError: codes.OK,
		},
		{
			name: "missing sgc_id",
			request: &pb.InstallAddonRequest{
				AddonId: 100,
			},
			mockSetup:     func(m *MockWorkshopManager) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "missing addon_id",
			request: &pb.InstallAddonRequest{
				SgcId: 1,
			},
			mockSetup:     func(m *MockWorkshopManager) {},
			expectedError: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockManager := new(MockWorkshopManager)
			tt.mockSetup(mockManager)

			handler := &WorkshopServiceHandler{
				workshopManager: mockManager,
			}

			resp, err := handler.InstallAddon(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.Installation)
			}

			mockManager.AssertExpectations(t)
		})
	}
}

// TestBatchCreateAddons tests the BatchCreateAddons RPC's request validation
// and its delegation to WorkshopManager.BatchCreateAddons (FR2/FR3): the
// handler validates game_id/library_id/preset_id and otherwise treats
// partial per-item failure as a normal (non-error) response.
func TestBatchCreateAddons(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.BatchCreateAddonsRequest
		mockSetup     func(*MockWorkshopManager, *MockWorkshopLibraryRepository, *MockAddonPathPresetRepository)
		expectedError codes.Code
		checkResponse func(*testing.T, *pb.BatchCreateAddonsResponse)
	}{
		{
			name:          "missing game_id",
			request:       &pb.BatchCreateAddonsRequest{Entries: "111111"},
			mockSetup:     func(*MockWorkshopManager, *MockWorkshopLibraryRepository, *MockAddonPathPresetRepository) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name:          "missing entries",
			request:       &pb.BatchCreateAddonsRequest{GameId: 1},
			mockSetup:     func(*MockWorkshopManager, *MockWorkshopLibraryRepository, *MockAddonPathPresetRepository) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name:    "library not found",
			request: &pb.BatchCreateAddonsRequest{GameId: 1, Entries: "111111", LibraryId: 5},
			mockSetup: func(m *MockWorkshopManager, l *MockWorkshopLibraryRepository, p *MockAddonPathPresetRepository) {
				l.On("Get", mock.Anything, int64(5)).Return(nil, assert.AnError)
			},
			expectedError: codes.NotFound,
		},
		{
			name:    "preset not found",
			request: &pb.BatchCreateAddonsRequest{GameId: 1, Entries: "111111", PresetId: 9},
			mockSetup: func(m *MockWorkshopManager, l *MockWorkshopLibraryRepository, p *MockAddonPathPresetRepository) {
				p.On("Get", mock.Anything, int64(9)).Return(nil, assert.AnError)
			},
			expectedError: codes.NotFound,
		},
		{
			name:    "successful batch create with mixed per-item results",
			request: &pb.BatchCreateAddonsRequest{GameId: 1, Entries: "111111\nbadline"},
			mockSetup: func(m *MockWorkshopManager, l *MockWorkshopLibraryRepository, p *MockAddonPathPresetRepository) {
				addonID := int64(10)
				workshopID := "111111"
				errMsg := "not a numeric Workshop ID or recognized Workshop URL"
				m.On("BatchCreateAddons", mock.Anything, int64(1), int64(0), "111111\nbadline", int64(0)).
					Return(&manman.WorkshopBatchJob{
						BatchJobID:     1,
						TotalItems:     2,
						SucceededItems: 1,
						FailedItems:    1,
						Status:         "completed_with_errors",
					}, []*manman.WorkshopBatchJobItem{
						{RawInput: "111111", WorkshopID: &workshopID, AddonID: &addonID, Status: "succeeded"},
						{RawInput: "badline", Status: "failed", ErrorMessage: &errMsg},
					}, nil)
			},
			expectedError: codes.OK,
			checkResponse: func(t *testing.T, resp *pb.BatchCreateAddonsResponse) {
				assert.Equal(t, int64(1), resp.BatchJobId)
				assert.Equal(t, int32(2), resp.TotalItems)
				assert.Equal(t, int32(1), resp.SucceededItems)
				assert.Equal(t, int32(1), resp.FailedItems)
				if assert.Len(t, resp.Results, 2) {
					assert.Equal(t, "succeeded", resp.Results[0].Status)
					assert.Equal(t, "111111", resp.Results[0].WorkshopId)
					assert.Equal(t, int64(10), resp.Results[0].AddonId)
					assert.Equal(t, "failed", resp.Results[1].Status)
					assert.NotEmpty(t, resp.Results[1].ErrorMessage)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockManager := new(MockWorkshopManager)
			mockLibraryRepo := new(MockWorkshopLibraryRepository)
			mockPresetRepo := new(MockAddonPathPresetRepository)
			tt.mockSetup(mockManager, mockLibraryRepo, mockPresetRepo)

			handler := &WorkshopServiceHandler{
				workshopManager: mockManager,
				libraryRepo:     mockLibraryRepo,
				presetRepo:      mockPresetRepo,
			}

			resp, err := handler.BatchCreateAddons(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				if tt.checkResponse != nil {
					tt.checkResponse(t, resp)
				}
			}

			mockManager.AssertExpectations(t)
			mockLibraryRepo.AssertExpectations(t)
			mockPresetRepo.AssertExpectations(t)
		})
	}
}

// TestGetInstallation tests the GetInstallation RPC
func TestGetInstallation(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.GetInstallationRequest
		mockSetup     func(*MockWorkshopInstallationRepository)
		expectedError codes.Code
	}{
		{
			name: "successful get",
			request: &pb.GetInstallationRequest{
				InstallationId: 1,
			},
			mockSetup: func(m *MockWorkshopInstallationRepository) {
				m.On("Get", mock.Anything, int64(1)).
					Return(&manman.WorkshopInstallation{
						InstallationID:   1,
						SGCID:            1,
						AddonID:          100,
						Status:           manman.InstallationStatusInstalled,
						InstallationPath: "/path/to/addon",
					}, nil)
			},
			expectedError: codes.OK,
		},
		{
			name: "missing installation_id",
			request: &pb.GetInstallationRequest{
				InstallationId: 0,
			},
			mockSetup:     func(m *MockWorkshopInstallationRepository) {},
			expectedError: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRepo := new(MockWorkshopInstallationRepository)
			tt.mockSetup(mockRepo)

			handler := &WorkshopServiceHandler{
				installationRepo: mockRepo,
			}

			resp, err := handler.GetInstallation(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.Installation)
			}

			mockRepo.AssertExpectations(t)
		})
	}
}

// TestListInstallations tests the ListInstallations RPC
func TestListInstallations(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.ListInstallationsRequest
		mockSetup     func(*MockWorkshopInstallationRepository)
		expectedError codes.Code
		expectedCount int
	}{
		{
			name: "list by SGC",
			request: &pb.ListInstallationsRequest{
				SgcId: 1,
				Limit: 10,
			},
			mockSetup: func(m *MockWorkshopInstallationRepository) {
				m.On("ListBySGC", mock.Anything, int64(1), 10, 0).
					Return([]*manman.WorkshopInstallation{
						{InstallationID: 1, SGCID: 1, AddonID: 100},
						{InstallationID: 2, SGCID: 1, AddonID: 101},
					}, nil)
			},
			expectedError: codes.OK,
			expectedCount: 2,
		},
		{
			name: "list by addon",
			request: &pb.ListInstallationsRequest{
				AddonId: 100,
				Limit:   10,
			},
			mockSetup: func(m *MockWorkshopInstallationRepository) {
				m.On("ListByAddon", mock.Anything, int64(100), 10, 0).
					Return([]*manman.WorkshopInstallation{
						{InstallationID: 1, SGCID: 1, AddonID: 100},
					}, nil)
			},
			expectedError: codes.OK,
			expectedCount: 1,
		},
		{
			name: "list all",
			request: &pb.ListInstallationsRequest{
				Limit: 10,
			},
			mockSetup: func(m *MockWorkshopInstallationRepository) {
				m.On("List", mock.Anything, 10, 0).
					Return([]*manman.WorkshopInstallation{
						{InstallationID: 1, SGCID: 1, AddonID: 100},
						{InstallationID: 2, SGCID: 2, AddonID: 101},
					}, nil)
			},
			expectedError: codes.OK,
			expectedCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRepo := new(MockWorkshopInstallationRepository)
			tt.mockSetup(mockRepo)

			handler := &WorkshopServiceHandler{
				installationRepo: mockRepo,
			}

			resp, err := handler.ListInstallations(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
				assert.Equal(t, tt.expectedCount, len(resp.Installations))
			}

			mockRepo.AssertExpectations(t)
		})
	}
}

// TestRemoveInstallation tests the RemoveInstallation RPC
func TestRemoveInstallation(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.RemoveInstallationRequest
		mockSetup     func(*MockWorkshopManager)
		expectedError codes.Code
	}{
		{
			name: "successful removal",
			request: &pb.RemoveInstallationRequest{
				InstallationId: 1,
			},
			mockSetup: func(m *MockWorkshopManager) {
				m.On("RemoveInstallation", mock.Anything, int64(1)).Return(nil)
			},
			expectedError: codes.OK,
		},
		{
			name: "missing installation_id",
			request: &pb.RemoveInstallationRequest{
				InstallationId: 0,
			},
			mockSetup:     func(m *MockWorkshopManager) {},
			expectedError: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockManager := new(MockWorkshopManager)
			tt.mockSetup(mockManager)

			handler := &WorkshopServiceHandler{
				workshopManager: mockManager,
			}

			resp, err := handler.RemoveInstallation(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
			}

			mockManager.AssertExpectations(t)
		})
	}
}

// TestCreateLibrary tests the CreateLibrary RPC
func TestCreateLibrary(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.CreateLibraryRequest
		mockSetup     func(*MockWorkshopLibraryRepository)
		expectedError codes.Code
	}{
		{
			name: "successful creation",
			request: &pb.CreateLibraryRequest{
				GameId:      1,
				Name:        "Test Library",
				Description: "Test Description",
			},
			mockSetup: func(m *MockWorkshopLibraryRepository) {
				m.On("Create", mock.Anything, mock.MatchedBy(func(lib *manman.WorkshopLibrary) bool {
					return lib.GameID == 1 && lib.Name == "Test Library"
				})).Return(&manman.WorkshopLibrary{
					LibraryID: 1,
					GameID:    1,
					Name:      "Test Library",
				}, nil)
			},
			expectedError: codes.OK,
		},
		{
			name: "missing game_id",
			request: &pb.CreateLibraryRequest{
				Name: "Test Library",
			},
			mockSetup:     func(m *MockWorkshopLibraryRepository) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "missing name",
			request: &pb.CreateLibraryRequest{
				GameId: 1,
			},
			mockSetup:     func(m *MockWorkshopLibraryRepository) {},
			expectedError: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRepo := new(MockWorkshopLibraryRepository)
			tt.mockSetup(mockRepo)

			handler := &WorkshopServiceHandler{
				libraryRepo: mockRepo,
			}

			resp, err := handler.CreateLibrary(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.Library)
			}

			mockRepo.AssertExpectations(t)
		})
	}
}

// TestGetLibrary tests the GetLibrary RPC
func TestGetLibrary(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.GetLibraryRequest
		mockSetup     func(*MockWorkshopLibraryRepository)
		expectedError codes.Code
	}{
		{
			name: "successful get",
			request: &pb.GetLibraryRequest{
				LibraryId: 1,
			},
			mockSetup: func(m *MockWorkshopLibraryRepository) {
				m.On("Get", mock.Anything, int64(1)).
					Return(&manman.WorkshopLibrary{
						LibraryID: 1,
						GameID:    1,
						Name:      "Test Library",
					}, nil)
			},
			expectedError: codes.OK,
		},
		{
			name: "missing library_id",
			request: &pb.GetLibraryRequest{
				LibraryId: 0,
			},
			mockSetup:     func(m *MockWorkshopLibraryRepository) {},
			expectedError: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRepo := new(MockWorkshopLibraryRepository)
			tt.mockSetup(mockRepo)

			handler := &WorkshopServiceHandler{
				libraryRepo: mockRepo,
			}

			resp, err := handler.GetLibrary(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.Library)
			}

			mockRepo.AssertExpectations(t)
		})
	}
}

// TestListLibraries tests the ListLibraries RPC
func TestListLibraries(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.ListLibrariesRequest
		mockSetup     func(*MockWorkshopLibraryRepository)
		expectedError codes.Code
		expectedCount int
	}{
		{
			name: "list by game",
			request: &pb.ListLibrariesRequest{
				GameId: 1,
				Limit:  10,
			},
			mockSetup: func(m *MockWorkshopLibraryRepository) {
				gameID := int64(1)
				m.On("List", mock.Anything, &gameID, 10, 0).
					Return([]*manman.WorkshopLibrary{
						{LibraryID: 1, GameID: 1, Name: "Library 1"},
						{LibraryID: 2, GameID: 1, Name: "Library 2"},
					}, nil)
			},
			expectedError: codes.OK,
			expectedCount: 2,
		},
		{
			name: "list all",
			request: &pb.ListLibrariesRequest{
				Limit: 10,
			},
			mockSetup: func(m *MockWorkshopLibraryRepository) {
				m.On("List", mock.Anything, (*int64)(nil), 10, 0).
					Return([]*manman.WorkshopLibrary{
						{LibraryID: 1, GameID: 1, Name: "Library 1"},
					}, nil)
			},
			expectedError: codes.OK,
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRepo := new(MockWorkshopLibraryRepository)
			tt.mockSetup(mockRepo)

			handler := &WorkshopServiceHandler{
				libraryRepo: mockRepo,
			}

			resp, err := handler.ListLibraries(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
				assert.Equal(t, tt.expectedCount, len(resp.Libraries))
			}

			mockRepo.AssertExpectations(t)
		})
	}
}

// TestAddAddonToLibrary tests the AddAddonToLibrary RPC
func TestAddAddonToLibrary(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.AddAddonToLibraryRequest
		mockSetup     func(*MockWorkshopAddonRepository, *MockWorkshopLibraryRepository)
		expectedError codes.Code
	}{
		{
			name: "successful add",
			request: &pb.AddAddonToLibraryRequest{
				LibraryId:    1,
				AddonId:      100,
				DisplayOrder: 5,
			},
			mockSetup: func(a *MockWorkshopAddonRepository, l *MockWorkshopLibraryRepository) {
				// Addon has a preset, so path validation passes without needing library.Get
				a.On("Get", mock.Anything, int64(100)).Return(&manman.WorkshopAddon{
					AddonID:  100,
					Name:     "Test Addon",
					PresetID: 1,
				}, nil)
				l.On("AddAddon", mock.Anything, int64(1), int64(100), 5).Return(nil)
			},
			expectedError: codes.OK,
		},
		{
			name: "missing library_id",
			request: &pb.AddAddonToLibraryRequest{
				AddonId: 100,
			},
			mockSetup:     func(a *MockWorkshopAddonRepository, l *MockWorkshopLibraryRepository) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "missing addon_id",
			request: &pb.AddAddonToLibraryRequest{
				LibraryId: 1,
			},
			mockSetup:     func(a *MockWorkshopAddonRepository, l *MockWorkshopLibraryRepository) {},
			expectedError: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockAddonRepo := new(MockWorkshopAddonRepository)
			mockLibraryRepo := new(MockWorkshopLibraryRepository)
			tt.mockSetup(mockAddonRepo, mockLibraryRepo)

			handler := &WorkshopServiceHandler{
				addonRepo:   mockAddonRepo,
				libraryRepo: mockLibraryRepo,
			}

			resp, err := handler.AddAddonToLibrary(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
			}

			mockAddonRepo.AssertExpectations(t)
			mockLibraryRepo.AssertExpectations(t)
		})
	}
}

// TestRemoveAddonFromLibrary tests the RemoveAddonFromLibrary RPC
func TestRemoveAddonFromLibrary(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.RemoveAddonFromLibraryRequest
		mockSetup     func(*MockWorkshopLibraryRepository)
		expectedError codes.Code
	}{
		{
			name: "successful remove",
			request: &pb.RemoveAddonFromLibraryRequest{
				LibraryId: 1,
				AddonId:   100,
			},
			mockSetup: func(m *MockWorkshopLibraryRepository) {
				m.On("RemoveAddon", mock.Anything, int64(1), int64(100)).Return(nil)
			},
			expectedError: codes.OK,
		},
		{
			name: "missing library_id",
			request: &pb.RemoveAddonFromLibraryRequest{
				AddonId: 100,
			},
			mockSetup:     func(m *MockWorkshopLibraryRepository) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "missing addon_id",
			request: &pb.RemoveAddonFromLibraryRequest{
				LibraryId: 1,
			},
			mockSetup:     func(m *MockWorkshopLibraryRepository) {},
			expectedError: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRepo := new(MockWorkshopLibraryRepository)
			tt.mockSetup(mockRepo)

			handler := &WorkshopServiceHandler{
				libraryRepo: mockRepo,
			}

			resp, err := handler.RemoveAddonFromLibrary(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
			}

			mockRepo.AssertExpectations(t)
		})
	}
}

// TestAddLibraryReference tests the AddLibraryReference RPC
func TestAddLibraryReference(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.AddLibraryReferenceRequest
		mockSetup     func(*MockWorkshopLibraryRepository)
		expectedError codes.Code
	}{
		{
			name: "successful add reference",
			request: &pb.AddLibraryReferenceRequest{
				ParentLibraryId: 1,
				ChildLibraryId:  2,
			},
			mockSetup: func(m *MockWorkshopLibraryRepository) {
				m.On("DetectCircularReference", mock.Anything, int64(1), int64(2)).Return(false, nil)
				m.On("AddReference", mock.Anything, int64(1), int64(2)).Return(nil)
			},
			expectedError: codes.OK,
		},
		{
			name: "circular reference detected",
			request: &pb.AddLibraryReferenceRequest{
				ParentLibraryId: 1,
				ChildLibraryId:  2,
			},
			mockSetup: func(m *MockWorkshopLibraryRepository) {
				m.On("DetectCircularReference", mock.Anything, int64(1), int64(2)).Return(true, nil)
			},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "self reference",
			request: &pb.AddLibraryReferenceRequest{
				ParentLibraryId: 1,
				ChildLibraryId:  1,
			},
			mockSetup:     func(m *MockWorkshopLibraryRepository) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "missing parent_library_id",
			request: &pb.AddLibraryReferenceRequest{
				ChildLibraryId: 2,
			},
			mockSetup:     func(m *MockWorkshopLibraryRepository) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "missing child_library_id",
			request: &pb.AddLibraryReferenceRequest{
				ParentLibraryId: 1,
			},
			mockSetup:     func(m *MockWorkshopLibraryRepository) {},
			expectedError: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRepo := new(MockWorkshopLibraryRepository)
			tt.mockSetup(mockRepo)

			handler := &WorkshopServiceHandler{
				libraryRepo: mockRepo,
			}

			resp, err := handler.AddLibraryReference(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
			}

			mockRepo.AssertExpectations(t)
		})
	}
}

// TestFetchAddonMetadata tests the FetchAddonMetadata RPC
func TestFetchAddonMetadata(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.FetchAddonMetadataRequest
		mockSetup     func(*MockWorkshopManager)
		expectedError codes.Code
		checkResponse func(*testing.T, *pb.FetchAddonMetadataResponse)
	}{
		{
			name: "successful metadata fetch",
			request: &pb.FetchAddonMetadataRequest{
				GameId:     1,
				WorkshopId: "123456",
			},
			mockSetup: func(m *MockWorkshopManager) {
				description := "Test addon description"
				fileSize := int64(1024000)
				m.On("FetchMetadata", mock.Anything, int64(1), "123456").
					Return(&manman.WorkshopAddon{
						GameID:        1,
						WorkshopID:    "123456",
						PlatformType:  manman.PlatformTypeSteamWorkshop,
						Name:          "Test Addon",
						Description:   &description,
						FileSizeBytes: &fileSize,
						IsCollection:  false,
					}, nil)
			},
			expectedError: codes.OK,
			checkResponse: func(t *testing.T, resp *pb.FetchAddonMetadataResponse) {
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.Addon)
				assert.Equal(t, int64(1), resp.Addon.GameId)
				assert.Equal(t, "123456", resp.Addon.WorkshopId)
				assert.Equal(t, "Test Addon", resp.Addon.Name)
				assert.Equal(t, "Test addon description", resp.Addon.Description)
				assert.Equal(t, int64(1024000), resp.Addon.FileSizeBytes)
				assert.False(t, resp.Addon.IsCollection)
			},
		},
		{
			name: "successful collection metadata fetch",
			request: &pb.FetchAddonMetadataRequest{
				GameId:     1,
				WorkshopId: "789012",
			},
			mockSetup: func(m *MockWorkshopManager) {
				description := "Test collection"
				fileSize := int64(2048000)
				m.On("FetchMetadata", mock.Anything, int64(1), "789012").
					Return(&manman.WorkshopAddon{
						GameID:        1,
						WorkshopID:    "789012",
						PlatformType:  manman.PlatformTypeSteamWorkshop,
						Name:          "Test Collection",
						Description:   &description,
						FileSizeBytes: &fileSize,
						IsCollection:  true,
						Metadata: map[string]interface{}{
							"collection_items": []map[string]interface{}{
								{"workshop_id": "111", "title": "Item 1"},
								{"workshop_id": "222", "title": "Item 2"},
							},
						},
					}, nil)
			},
			expectedError: codes.OK,
			checkResponse: func(t *testing.T, resp *pb.FetchAddonMetadataResponse) {
				assert.NotNil(t, resp)
				assert.NotNil(t, resp.Addon)
				assert.Equal(t, "Test Collection", resp.Addon.Name)
				assert.True(t, resp.Addon.IsCollection)
			},
		},
		{
			name: "missing game_id",
			request: &pb.FetchAddonMetadataRequest{
				WorkshopId: "123456",
			},
			mockSetup:     func(m *MockWorkshopManager) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "missing workshop_id",
			request: &pb.FetchAddonMetadataRequest{
				GameId: 1,
			},
			mockSetup:     func(m *MockWorkshopManager) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "unsupported platform type",
			request: &pb.FetchAddonMetadataRequest{
				GameId:       1,
				WorkshopId:   "123456",
				PlatformType: "unsupported_platform",
			},
			mockSetup:     func(m *MockWorkshopManager) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "steam API failure",
			request: &pb.FetchAddonMetadataRequest{
				GameId:     1,
				WorkshopId: "999999",
			},
			mockSetup: func(m *MockWorkshopManager) {
				m.On("FetchMetadata", mock.Anything, int64(1), "999999").
					Return(nil, assert.AnError)
			},
			expectedError: codes.Unavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockManager := new(MockWorkshopManager)
			tt.mockSetup(mockManager)

			handler := &WorkshopServiceHandler{
				workshopManager: mockManager,
			}

			resp, err := handler.FetchAddonMetadata(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				if tt.checkResponse != nil {
					tt.checkResponse(t, resp)
				}
			}

			mockManager.AssertExpectations(t)
		})
	}
}

// TestGetBatchJob covers the GetBatchJob RPC (#2179, plan #2175, FR4):
// items must reach the response in the order the repository returns them
// (display_order is the repository's responsibility -- ListBatchJobItems
// -- this test guards that the handler does not reorder or drop them), a
// mixed-status job's aggregate counts must pass through unchanged, and an
// unknown batch_job_id must surface as codes.NotFound.
func TestGetBatchJob(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name          string
		request       *pb.GetBatchJobRequest
		mockSetup     func(*MockWorkshopBatchJobRepository)
		expectedError codes.Code
		checkResponse func(*testing.T, *pb.GetBatchJobResponse)
	}{
		{
			name: "items returned in display_order, mixed-status counts pass through",
			request: &pb.GetBatchJobRequest{
				BatchJobId: 1,
			},
			mockSetup: func(m *MockWorkshopBatchJobRepository) {
				libraryID := int64(9)
				sourceInput := "collection paste"
				m.On("GetBatchJob", mock.Anything, int64(1)).
					Return(&manman.WorkshopBatchJob{
						BatchJobID:     1,
						JobType:        "batch_create",
						GameID:         1,
						LibraryID:      &libraryID,
						SourceInput:    &sourceInput,
						Status:         "completed_with_errors",
						TotalItems:     3,
						SucceededItems: 2,
						FailedItems:    1,
						CreatedAt:      now,
						UpdatedAt:      now,
					}, nil)

				workshopID1 := "111"
				addonID1 := int64(101)
				errMsg := "workshop item not found"
				m.On("ListBatchJobItems", mock.Anything, int64(1)).
					Return([]*manman.WorkshopBatchJobItem{
						{BatchJobItemID: 1, BatchJobID: 1, RawInput: "111", WorkshopID: &workshopID1, AddonID: &addonID1, Status: "succeeded", DisplayOrder: 0},
						{BatchJobItemID: 2, BatchJobID: 1, RawInput: "222", Status: "failed", ErrorMessage: &errMsg, DisplayOrder: 1},
						{BatchJobItemID: 3, BatchJobID: 1, RawInput: "333", Status: "succeeded", DisplayOrder: 2},
					}, nil)
			},
			expectedError: codes.OK,
			checkResponse: func(t *testing.T, resp *pb.GetBatchJobResponse) {
				assert.NotNil(t, resp.Job)
				assert.Equal(t, "completed_with_errors", resp.Job.Status)
				assert.Equal(t, int32(3), resp.Job.TotalItems)
				assert.Equal(t, int32(2), resp.Job.SucceededItems)
				assert.Equal(t, int32(1), resp.Job.FailedItems)

				if assert.Len(t, resp.Items, 3) {
					assert.Equal(t, "111", resp.Items[0].RawInput)
					assert.Equal(t, "succeeded", resp.Items[0].Status)
					assert.Equal(t, "222", resp.Items[1].RawInput)
					assert.Equal(t, "failed", resp.Items[1].Status)
					assert.Equal(t, "workshop item not found", resp.Items[1].ErrorMessage)
					assert.Equal(t, "333", resp.Items[2].RawInput)
				}
			},
		},
		{
			name: "missing batch_job_id",
			request: &pb.GetBatchJobRequest{
				BatchJobId: 0,
			},
			mockSetup:     func(m *MockWorkshopBatchJobRepository) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "unknown batch_job_id",
			request: &pb.GetBatchJobRequest{
				BatchJobId: 999,
			},
			mockSetup: func(m *MockWorkshopBatchJobRepository) {
				m.On("GetBatchJob", mock.Anything, int64(999)).
					Return(nil, assert.AnError)
			},
			expectedError: codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRepo := new(MockWorkshopBatchJobRepository)
			tt.mockSetup(mockRepo)

			handler := &WorkshopServiceHandler{
				batchJobRepo: mockRepo,
			}

			resp, err := handler.GetBatchJob(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				if tt.checkResponse != nil {
					tt.checkResponse(t, resp)
				}
			}

			mockRepo.AssertExpectations(t)
		})
	}
}

// TestListBatchJobs covers the ListBatchJobs RPC (#2179, plan #2175, FR4):
// the requested limit reaches the repository, and jobs come back in the
// order the repository returns them (newest-first is the repository's
// ORDER BY -- this test guards that the handler passes that ordering
// through unchanged rather than re-sorting or truncating).
func TestListBatchJobs(t *testing.T) {
	tests := []struct {
		name          string
		request       *pb.ListBatchJobsRequest
		mockSetup     func(*MockWorkshopBatchJobRepository)
		expectedError codes.Code
		checkResponse func(*testing.T, *pb.ListBatchJobsResponse)
	}{
		{
			name: "respects limit and preserves newest-first order",
			request: &pb.ListBatchJobsRequest{
				GameId: 1,
				Limit:  2,
			},
			mockSetup: func(m *MockWorkshopBatchJobRepository) {
				m.On("ListBatchJobs", mock.Anything, int64(1), 2).
					Return([]*manman.WorkshopBatchJob{
						{BatchJobID: 5, GameID: 1, JobType: "collection_add", Status: "completed"},
						{BatchJobID: 4, GameID: 1, JobType: "batch_create", Status: "completed"},
					}, nil)
			},
			expectedError: codes.OK,
			checkResponse: func(t *testing.T, resp *pb.ListBatchJobsResponse) {
				if assert.Len(t, resp.Jobs, 2) {
					assert.Equal(t, int64(5), resp.Jobs[0].BatchJobId)
					assert.Equal(t, int64(4), resp.Jobs[1].BatchJobId)
				}
			},
		},
		{
			name: "missing game_id",
			request: &pb.ListBatchJobsRequest{
				Limit: 10,
			},
			mockSetup:     func(m *MockWorkshopBatchJobRepository) {},
			expectedError: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRepo := new(MockWorkshopBatchJobRepository)
			tt.mockSetup(mockRepo)

			handler := &WorkshopServiceHandler{
				batchJobRepo: mockRepo,
			}

			resp, err := handler.ListBatchJobs(context.Background(), tt.request)

			if tt.expectedError != codes.OK {
				assert.Error(t, err)
				st, ok := status.FromError(err)
				assert.True(t, ok)
				assert.Equal(t, tt.expectedError, st.Code())
			} else {
				assert.NoError(t, err)
				if tt.checkResponse != nil {
					tt.checkResponse(t, resp)
				}
			}

			mockRepo.AssertExpectations(t)
		})
	}
}
