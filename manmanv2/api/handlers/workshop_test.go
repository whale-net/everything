package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/whale-net/everything/manmanv2"
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

func (m *MockWorkshopAddonRepository) Update(ctx context.Context, addon *manman.WorkshopAddon) error {
	args := m.Called(ctx, addon)
	return args.Error(0)
}

func (m *MockWorkshopAddonRepository) Delete(ctx context.Context, addonID int64) error {
	args := m.Called(ctx, addonID)
	return args.Error(0)
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

func (m *MockWorkshopLibraryRepository) ListAddons(ctx context.Context, libraryID int64) ([]*manman.WorkshopAddon, error) {
	args := m.Called(ctx, libraryID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*manman.WorkshopAddon), args.Error(1)
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

// MockWorkshopManager is a mock implementation of WorkshopManager
type MockWorkshopManager struct {
	mock.Mock
}

func (m *MockWorkshopManager) InstallAddon(ctx context.Context, sgcID, addonID int64, forceReinstall bool) (*manman.WorkshopInstallation, error) {
	args := m.Called(ctx, sgcID, addonID, forceReinstall)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopInstallation), args.Error(1)
}

func (m *MockWorkshopManager) RemoveInstallation(ctx context.Context, installationID int64) error {
	args := m.Called(ctx, installationID)
	return args.Error(0)
}

func (m *MockWorkshopManager) FetchMetadata(ctx context.Context, gameID int64, workshopID string) (*manman.WorkshopAddon, error) {
	args := m.Called(ctx, gameID, workshopID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*manman.WorkshopAddon), args.Error(1)
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
				m.On("InstallAddon", mock.Anything, int64(1), int64(100), false).
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
		mockSetup     func(*MockWorkshopLibraryRepository)
		expectedError codes.Code
	}{
		{
			name: "successful add",
			request: &pb.AddAddonToLibraryRequest{
				LibraryId:    1,
				AddonId:      100,
				DisplayOrder: 5,
			},
			mockSetup: func(m *MockWorkshopLibraryRepository) {
				m.On("AddAddon", mock.Anything, int64(1), int64(100), 5).Return(nil)
			},
			expectedError: codes.OK,
		},
		{
			name: "missing library_id",
			request: &pb.AddAddonToLibraryRequest{
				AddonId: 100,
			},
			mockSetup:     func(m *MockWorkshopLibraryRepository) {},
			expectedError: codes.InvalidArgument,
		},
		{
			name: "missing addon_id",
			request: &pb.AddAddonToLibraryRequest{
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

			mockRepo.AssertExpectations(t)
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
