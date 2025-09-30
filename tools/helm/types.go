package helm

import (
	"fmt"
	"strings"
)

// AppType represents the type of application being deployed
type AppType string

const (
	// ExternalAPI is an API service exposed via Ingress
	ExternalAPI AppType = "external-api"

	// InternalAPI is an API service only accessible within the cluster
	InternalAPI AppType = "internal-api"

	// Worker is a background processor without service exposure
	Worker AppType = "worker"

	// Job is a one-time or scheduled job (e.g., migrations)
	Job AppType = "job"
)

// String returns the string representation of the AppType
func (t AppType) String() string {
	return string(t)
}

// IsValid checks if the AppType is valid
func (t AppType) IsValid() bool {
	switch t {
	case ExternalAPI, InternalAPI, Worker, Job:
		return true
	default:
		return false
	}
}

// RequiresDeployment returns true if this app type uses a Deployment
func (t AppType) RequiresDeployment() bool {
	switch t {
	case ExternalAPI, InternalAPI, Worker:
		return true
	case Job:
		return false
	default:
		return false
	}
}

// RequiresService returns true if this app type needs a Service
func (t AppType) RequiresService() bool {
	switch t {
	case ExternalAPI, InternalAPI:
		return true
	case Worker, Job:
		return false
	default:
		return false
	}
}

// RequiresIngress returns true if this app type should have an Ingress
func (t AppType) RequiresIngress() bool {
	return t == ExternalAPI
}

// RequiresJob returns true if this app type is a Job
func (t AppType) RequiresJob() bool {
	return t == Job
}

// RequiresPDB returns true if this app type should have a PodDisruptionBudget
func (t AppType) RequiresPDB() bool {
	switch t {
	case ExternalAPI, InternalAPI, Worker:
		return true
	case Job:
		return false
	default:
		return false
	}
}

// ResolveAppType determines the app type, with explicit type taking precedence over inference.
// If appTypeStr is provided and valid, it's used directly.
// Otherwise, the app type is inferred from the app name.
// Returns the resolved AppType and any validation error.
func ResolveAppType(appName string, appTypeStr string) (AppType, error) {
	// If explicit app type is provided, use it (with validation)
	if appTypeStr != "" {
		appType, err := ParseAppType(appTypeStr)
		if err != nil {
			return "", err
		}
		return appType, nil
	}

	// Otherwise, infer from app name
	return InferAppType(appName), nil
}

// InferAppType attempts to infer the app type from the app name
// Returns InternalAPI as default if inference fails
// DEPRECATED: Use ResolveAppType instead to support explicit app types
func InferAppType(appName string) AppType {
	lowerName := strings.ToLower(appName)

	// Check for job patterns first (highest priority)
	if strings.Contains(lowerName, "migration") ||
		strings.Contains(lowerName, "job") ||
		strings.HasSuffix(lowerName, "-migrate") {
		return Job
	}

	// Check for API patterns before worker patterns
	// This ensures "worker-api" is classified as API, not worker
	if strings.Contains(lowerName, "api") {
		// Experience API is typically external-facing
		if strings.Contains(lowerName, "experience") ||
			strings.Contains(lowerName, "external") ||
			strings.Contains(lowerName, "public") {
			return ExternalAPI
		}
		return InternalAPI
	}

	// Check for worker patterns (after API check)
	if strings.Contains(lowerName, "worker") ||
		strings.Contains(lowerName, "processor") ||
		strings.Contains(lowerName, "consumer") {
		return Worker
	}

	// Default to internal-api for unknown patterns
	return InternalAPI
}

// ParseAppType converts a string to AppType with validation
func ParseAppType(s string) (AppType, error) {
	t := AppType(s)
	if !t.IsValid() {
		return "", fmt.Errorf("invalid app type: %s (must be one of: external-api, internal-api, worker, job)", s)
	}
	return t, nil
}

// TemplateArtifacts returns the list of template files needed for this app type
func (t AppType) TemplateArtifacts() []string {
	var artifacts []string

	if t.RequiresDeployment() {
		artifacts = append(artifacts, "deployment.yaml")
	}

	if t.RequiresService() {
		artifacts = append(artifacts, "service.yaml")
	}

	if t.RequiresIngress() {
		artifacts = append(artifacts, "ingress.yaml")
	}

	if t.RequiresJob() {
		artifacts = append(artifacts, "job.yaml")
	}

	if t.RequiresPDB() {
		artifacts = append(artifacts, "pdb.yaml")
	}

	return artifacts
}

// ResourceConfig defines resource requests and limits
type ResourceConfig struct {
	RequestsCPU    string `json:"requests_cpu"`
	RequestsMemory string `json:"requests_memory"`
	LimitsCPU      string `json:"limits_cpu"`
	LimitsMemory   string `json:"limits_memory"`
}

// DefaultResourceConfig returns sensible defaults based on app type
func (t AppType) DefaultResourceConfig() ResourceConfig {
	switch t {
	case ExternalAPI, InternalAPI:
		return ResourceConfig{
			RequestsCPU:    "50m",
			RequestsMemory: "256Mi",
			LimitsCPU:      "100m",
			LimitsMemory:   "512Mi",
		}
	case Worker:
		return ResourceConfig{
			RequestsCPU:    "50m",
			RequestsMemory: "256Mi",
			LimitsCPU:      "100m",
			LimitsMemory:   "512Mi",
		}
	case Job:
		return ResourceConfig{
			RequestsCPU:    "100m",
			RequestsMemory: "256Mi",
			LimitsCPU:      "200m",
			LimitsMemory:   "512Mi",
		}
	default:
		return ResourceConfig{
			RequestsCPU:    "50m",
			RequestsMemory: "128Mi",
			LimitsCPU:      "100m",
			LimitsMemory:   "256Mi",
		}
	}
}
