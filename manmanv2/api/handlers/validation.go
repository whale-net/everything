package handlers

import (
	"context"

	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

type ValidationHandler struct {
	serverRepo     repository.ServerRepository
	gameConfigRepo repository.GameConfigRepository
	// portRepo and capabilityRepo will be added in Phase 5
}

func NewValidationHandler(
	serverRepo repository.ServerRepository,
	gameConfigRepo repository.GameConfigRepository,
) *ValidationHandler {
	return &ValidationHandler{
		serverRepo:     serverRepo,
		gameConfigRepo: gameConfigRepo,
	}
}

func (h *ValidationHandler) ValidateDeployment(ctx context.Context, req *pb.ValidateDeploymentRequest) (*pb.ValidateDeploymentResponse, error) {
	issues := []*pb.ValidationIssue{}

	// 1. Check server exists and is online
	server, err := h.serverRepo.Get(ctx, req.ServerId)
	if err != nil {
		return &pb.ValidateDeploymentResponse{
			Valid: false,
			Issues: []*pb.ValidationIssue{{
				Severity: pb.ValidationSeverity_VALIDATION_SEVERITY_ERROR,
				Field:    "server_id",
				Message:  "Server not found",
			}},
		}, nil
	}

	if server.Status != "online" {
		issues = append(issues, &pb.ValidationIssue{
			Severity:   pb.ValidationSeverity_VALIDATION_SEVERITY_WARNING,
			Field:      "server_id",
			Message:    "Server is offline",
			Suggestion: "Wait for server to come online or choose different server",
		})
	}

	// 2. Check game config exists
	_, err = h.gameConfigRepo.Get(ctx, req.GameConfigId)
	if err != nil {
		return &pb.ValidateDeploymentResponse{
			Valid: false,
			Issues: []*pb.ValidationIssue{{
				Severity: pb.ValidationSeverity_VALIDATION_SEVERITY_ERROR,
				Field:    "game_config_id",
				Message:  "Game config not found",
			}},
		}, nil
	}

	// 3. Validate port availability (TODO: requires port repository)
	// for _, binding := range req.PortBindings {
	//     inUse, err := h.portRepo.IsPortInUse(ctx, req.ServerId, binding.HostPort, binding.Protocol)
	//     if err != nil {
	//         continue
	//     }
	//     if inUse {
	//         issues = append(issues, &pb.ValidationIssue{
	//             Severity:   pb.ValidationSeverity_VALIDATION_SEVERITY_ERROR,
	//             Field:      "port_bindings",
	//             Message:    fmt.Sprintf("Port %d/%s already in use", binding.HostPort, binding.Protocol),
	//             Suggestion: "Choose a different port or stop conflicting service",
	//         })
	//     }
	// }

	// 4. Estimate resources
	estimate := &pb.DeploymentEstimate{
		EstimatedMemoryMb:      1024, // Default estimate
		EstimatedCpuMillicores: 500,  // Default estimate
	}
	for _, binding := range req.PortBindings {
		estimate.AllocatedPorts = append(estimate.AllocatedPorts, binding.HostPort)
	}

	// Determine overall validity
	valid := true
	for _, issue := range issues {
		if issue.Severity == pb.ValidationSeverity_VALIDATION_SEVERITY_ERROR {
			valid = false
			break
		}
	}

	return &pb.ValidateDeploymentResponse{
		Valid:    valid,
		Issues:   issues,
		Estimate: estimate,
	}, nil
}

