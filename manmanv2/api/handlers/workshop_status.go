package handlers

import (
	"context"
	"encoding/json"
	"log"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/api/repository"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
)

// WorkshopStatusHandler handles workshop installation status updates from host managers
type WorkshopStatusHandler struct {
	installationRepo repository.WorkshopInstallationRepository
	consumer         *rmq.Consumer
}

// NewWorkshopStatusHandler creates a new workshop status handler
func NewWorkshopStatusHandler(installationRepo repository.WorkshopInstallationRepository, rmqConn *rmq.Connection) (*WorkshopStatusHandler, error) {
	// Create consumer for workshop installation status updates
	consumer, err := rmq.NewConsumerWithOpts(rmqConn, "workshop.installation.status", false, false, 0, 0)
	if err != nil {
		return nil, err
	}

	handler := &WorkshopStatusHandler{
		installationRepo: installationRepo,
		consumer:         consumer,
	}

	// Register message handler
	consumer.RegisterHandler("workshop.installation.status", handler.handleStatusUpdate)

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
		log.Printf("Failed to unmarshal installation status update: %v", err)
		return err
	}

	log.Printf("Received installation status update: installation_id=%d, status=%s, progress=%d%%",
		update.InstallationID, update.Status, update.ProgressPercent)

	// Update installation status in database
	if err := h.installationRepo.UpdateStatus(ctx, update.InstallationID, update.Status, update.ErrorMessage); err != nil {
		log.Printf("Failed to update installation status: %v", err)
		return err
	}

	// Update progress if provided
	if update.ProgressPercent > 0 {
		if err := h.installationRepo.UpdateProgress(ctx, update.InstallationID, update.ProgressPercent); err != nil {
			log.Printf("Failed to update installation progress: %v", err)
			return err
		}
	}

	return nil
}
