package workshop

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/workshop"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

// WorkshopServiceHandler handles workshop addon management RPCs
type WorkshopServiceHandler struct {
	pb.UnimplementedWorkshopServiceServer
	addonRepo        repository.WorkshopAddonRepository
	installationRepo repository.WorkshopInstallationRepository
	libraryRepo      repository.WorkshopLibraryRepository
	sgcRepo          repository.ServerGameConfigRepository
	presetRepo       repository.AddonPathPresetRepository
	workshopManager  workshop.WorkshopManagerInterface
}

// NewWorkshopServiceHandler creates a new WorkshopServiceHandler
func NewWorkshopServiceHandler(
	addonRepo repository.WorkshopAddonRepository,
	installationRepo repository.WorkshopInstallationRepository,
	libraryRepo repository.WorkshopLibraryRepository,
	sgcRepo repository.ServerGameConfigRepository,
	presetRepo repository.AddonPathPresetRepository,
	workshopManager *workshop.WorkshopManager,
) *WorkshopServiceHandler {
	return &WorkshopServiceHandler{
		addonRepo:        addonRepo,
		installationRepo: installationRepo,
		libraryRepo:      libraryRepo,
		sgcRepo:          sgcRepo,
		presetRepo:       presetRepo,
		workshopManager:  workshopManager,
	}
}

// WorkshopStatusHandler handles workshop installation status updates from host managers
type WorkshopStatusHandler struct {
	installationRepo repository.WorkshopInstallationRepository
	consumer         *rmq.Consumer
}

// NewWorkshopStatusHandler creates a new workshop status handler
func NewWorkshopStatusHandler(installationRepo repository.WorkshopInstallationRepository, rmqConn *rmq.Connection) (*WorkshopStatusHandler, error) {
	consumer, err := rmq.NewConsumerWithOpts(rmqConn, "workshop.installation.status", false, false, 0, 0)
	if err != nil {
		return nil, err
	}

	if err := consumer.BindExchange("manman", []string{"status.workshop.installation.#"}); err != nil {
		consumer.Close()
		return nil, err
	}

	handler := &WorkshopStatusHandler{
		installationRepo: installationRepo,
		consumer:         consumer,
	}

	consumer.RegisterHandler("status.workshop.installation.#", handler.handleStatusUpdate)

	return handler, nil
}

// Start starts consuming status update messages
func (h *WorkshopStatusHandler) Start(ctx context.Context) error {
	return h.consumer.Start(ctx)
}

// Close closes the consumer
func (h *WorkshopStatusHandler) Close() error {
	return h.consumer.Close()
}

// handleStatusUpdate processes installation status update messages
func (h *WorkshopStatusHandler) handleStatusUpdate(ctx context.Context, msg rmq.Message) error {
	var update hostrmq.InstallationStatusUpdate
	if err := json.Unmarshal(msg.Body, &update); err != nil {
		slog.Warn("failed to unmarshal installation status update", "error", err)
		return err
	}

	slog.Info("received installation status update",
		"installation_id", update.InstallationID, "status", update.Status, "progress_percent", update.ProgressPercent)

	if err := h.installationRepo.UpdateStatus(ctx, update.InstallationID, update.Status, update.ErrorMessage); err != nil {
		slog.Warn("failed to update installation status", "installation_id", update.InstallationID, "status", update.Status, "error", err)
		return err
	}

	if update.ProgressPercent > 0 {
		if err := h.installationRepo.UpdateProgress(ctx, update.InstallationID, update.ProgressPercent); err != nil {
			slog.Warn("failed to update installation progress", "installation_id", update.InstallationID, "progress_percent", update.ProgressPercent, "error", err)
			return err
		}
	}

	return nil
}
