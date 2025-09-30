package helm

import (
	"testing"
)

func TestAppType_IsValid(t *testing.T) {
	tests := []struct {
		name     string
		appType  AppType
		expected bool
	}{
		{"ExternalAPI is valid", ExternalAPI, true},
		{"InternalAPI is valid", InternalAPI, true},
		{"Worker is valid", Worker, true},
		{"Job is valid", Job, true},
		{"Invalid type", AppType("invalid"), false},
		{"Empty type", AppType(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.appType.IsValid(); got != tt.expected {
				t.Errorf("AppType.IsValid() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestAppType_RequiresDeployment(t *testing.T) {
	tests := []struct {
		name     string
		appType  AppType
		expected bool
	}{
		{"ExternalAPI requires deployment", ExternalAPI, true},
		{"InternalAPI requires deployment", InternalAPI, true},
		{"Worker requires deployment", Worker, true},
		{"Job does not require deployment", Job, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.appType.RequiresDeployment(); got != tt.expected {
				t.Errorf("AppType.RequiresDeployment() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestAppType_RequiresService(t *testing.T) {
	tests := []struct {
		name     string
		appType  AppType
		expected bool
	}{
		{"ExternalAPI requires service", ExternalAPI, true},
		{"InternalAPI requires service", InternalAPI, true},
		{"Worker does not require service", Worker, false},
		{"Job does not require service", Job, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.appType.RequiresService(); got != tt.expected {
				t.Errorf("AppType.RequiresService() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestAppType_RequiresIngress(t *testing.T) {
	tests := []struct {
		name     string
		appType  AppType
		expected bool
	}{
		{"ExternalAPI requires ingress", ExternalAPI, true},
		{"InternalAPI does not require ingress", InternalAPI, false},
		{"Worker does not require ingress", Worker, false},
		{"Job does not require ingress", Job, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.appType.RequiresIngress(); got != tt.expected {
				t.Errorf("AppType.RequiresIngress() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestAppType_RequiresPDB(t *testing.T) {
	tests := []struct {
		name     string
		appType  AppType
		expected bool
	}{
		{"ExternalAPI requires PDB", ExternalAPI, true},
		{"InternalAPI requires PDB", InternalAPI, true},
		{"Worker requires PDB", Worker, true},
		{"Job does not require PDB", Job, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.appType.RequiresPDB(); got != tt.expected {
				t.Errorf("AppType.RequiresPDB() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestInferAppType(t *testing.T) {
	tests := []struct {
		name     string
		appName  string
		expected AppType
	}{
		{"Migration job", "manman-migrations", Job},
		{"Explicit job", "cleanup-job", Job},
		{"Worker pattern", "status-processor", Worker},
		{"Worker pattern 2", "event-worker", Worker},
		{"Consumer pattern", "message-consumer", Worker},
		{"Experience API", "experience-api", ExternalAPI},
		{"External API", "external-api", ExternalAPI},
		{"Public API", "public-api", ExternalAPI},
		{"Internal API", "status-api", InternalAPI},
		{"Worker DAL API", "worker-dal-api", InternalAPI},
		{"Generic API", "api-service", InternalAPI},
		{"Unknown pattern", "some-service", InternalAPI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InferAppType(tt.appName); got != tt.expected {
				t.Errorf("InferAppType(%q) = %v, want %v", tt.appName, got, tt.expected)
			}
		})
	}
}

func TestParseAppType(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  AppType
		shouldErr bool
	}{
		{"Valid external-api", "external-api", ExternalAPI, false},
		{"Valid internal-api", "internal-api", InternalAPI, false},
		{"Valid worker", "worker", Worker, false},
		{"Valid job", "job", Job, false},
		{"Invalid type", "invalid", "", true},
		{"Empty string", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAppType(tt.input)
			if tt.shouldErr {
				if err == nil {
					t.Errorf("ParseAppType(%q) expected error, got nil", tt.input)
				}
			} else {
				if err != nil {
					t.Errorf("ParseAppType(%q) unexpected error: %v", tt.input, err)
				}
				if got != tt.expected {
					t.Errorf("ParseAppType(%q) = %v, want %v", tt.input, got, tt.expected)
				}
			}
		})
	}
}

func TestAppType_TemplateArtifacts(t *testing.T) {
	tests := []struct {
		name     string
		appType  AppType
		expected []string
	}{
		{
			"ExternalAPI has all artifacts",
			ExternalAPI,
			[]string{"deployment.yaml", "service.yaml", "ingress.yaml", "pdb.yaml"},
		},
		{
			"InternalAPI has service but no ingress",
			InternalAPI,
			[]string{"deployment.yaml", "service.yaml", "pdb.yaml"},
		},
		{
			"Worker has only deployment and PDB",
			Worker,
			[]string{"deployment.yaml", "pdb.yaml"},
		},
		{
			"Job has only job template",
			Job,
			[]string{"job.yaml"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.appType.TemplateArtifacts()
			if len(got) != len(tt.expected) {
				t.Errorf("AppType.TemplateArtifacts() returned %d artifacts, want %d", len(got), len(tt.expected))
				return
			}
			for i, artifact := range got {
				if artifact != tt.expected[i] {
					t.Errorf("AppType.TemplateArtifacts()[%d] = %v, want %v", i, artifact, tt.expected[i])
				}
			}
		})
	}
}

func TestAppType_DefaultResourceConfig(t *testing.T) {
	tests := []struct {
		name    string
		appType AppType
	}{
		{"ExternalAPI has defaults", ExternalAPI},
		{"InternalAPI has defaults", InternalAPI},
		{"Worker has defaults", Worker},
		{"Job has defaults", Job},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.appType.DefaultResourceConfig()
			if got.RequestsCPU == "" {
				t.Errorf("DefaultResourceConfig() RequestsCPU is empty")
			}
			if got.RequestsMemory == "" {
				t.Errorf("DefaultResourceConfig() RequestsMemory is empty")
			}
			if got.LimitsCPU == "" {
				t.Errorf("DefaultResourceConfig() LimitsCPU is empty")
			}
			if got.LimitsMemory == "" {
				t.Errorf("DefaultResourceConfig() LimitsMemory is empty")
			}
		})
	}
}

func TestResolveAppType(t *testing.T) {
	tests := []struct {
		name        string
		appName     string
		appTypeStr  string
		expected    AppType
		shouldError bool
	}{
		// Explicit app types (should take precedence)
		{
			"Explicit external-api",
			"my-worker-service", // Name would infer as worker
			"external-api",
			ExternalAPI,
			false,
		},
		{
			"Explicit internal-api",
			"my-processor", // Name would infer as worker
			"internal-api",
			InternalAPI,
			false,
		},
		{
			"Explicit worker",
			"my-api", // Name would infer as internal-api
			"worker",
			Worker,
			false,
		},
		{
			"Explicit job",
			"status-api", // Name would infer as internal-api
			"job",
			Job,
			false,
		},
		// Inference when no explicit type (empty string)
		{
			"Infer external-api from experience-api",
			"experience-api",
			"",
			ExternalAPI,
			false,
		},
		{
			"Infer internal-api from status-api",
			"status-api",
			"",
			InternalAPI,
			false,
		},
		{
			"Infer internal-api from worker-dal-api",
			"worker-dal-api",
			"",
			InternalAPI,
			false,
		},
		{
			"Infer worker from status-processor",
			"status-processor",
			"",
			Worker,
			false,
		},
		{
			"Infer worker from background-worker",
			"background-worker",
			"",
			Worker,
			false,
		},
		{
			"Infer job from db-migrations",
			"db-migrations",
			"",
			Job,
			false,
		},
		{
			"Infer internal-api as default",
			"unknown-app",
			"",
			InternalAPI,
			false,
		},
		// Invalid explicit types
		{
			"Invalid explicit type",
			"my-app",
			"invalid-type",
			"",
			true,
		},
		{
			"Malformed explicit type",
			"my-app",
			"api",
			"",
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveAppType(tt.appName, tt.appTypeStr)
			if tt.shouldError {
				if err == nil {
					t.Errorf("ResolveAppType(%q, %q) expected error, got nil", tt.appName, tt.appTypeStr)
				}
			} else {
				if err != nil {
					t.Errorf("ResolveAppType(%q, %q) unexpected error: %v", tt.appName, tt.appTypeStr, err)
				}
				if got != tt.expected {
					t.Errorf("ResolveAppType(%q, %q) = %v, want %v", tt.appName, tt.appTypeStr, got, tt.expected)
				}
			}
		})
	}
}
