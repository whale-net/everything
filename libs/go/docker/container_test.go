package docker

import (
	"strings"
	"testing"

	"github.com/docker/docker/api/types/mount"
)

func TestVolumeMountTypeDetection(t *testing.T) {
	tests := []struct {
		name           string
		volumeString   string
		expectedType   mount.Type
		expectedSource string
		expectedTarget string
	}{
		{
			name:           "named volume",
			volumeString:   "manman-sgc-dev-7-cfg:/cfg",
			expectedType:   mount.TypeVolume,
			expectedSource: "manman-sgc-dev-7-cfg",
			expectedTarget: "/cfg",
		},
		{
			name:           "absolute path bind mount",
			volumeString:   "/var/lib/manman/data:/data",
			expectedType:   mount.TypeBind,
			expectedSource: "/var/lib/manman/data",
			expectedTarget: "/data",
		},
		{
			name:           "relative path bind mount",
			volumeString:   "./data:/data",
			expectedType:   mount.TypeBind,
			expectedSource: "./data",
			expectedTarget: "/data",
		},
		{
			name:           "named volume with hyphens",
			volumeString:   "my-app-data:/app/data",
			expectedType:   mount.TypeVolume,
			expectedSource: "my-app-data",
			expectedTarget: "/app/data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the logic from CreateContainer
			parts := strings.SplitN(tt.volumeString, ":", 2)
			if len(parts) != 2 {
				t.Fatalf("Invalid volume format: %s", tt.volumeString)
			}

			source := parts[0]
			target := parts[1]

			// Determine mount type
			mountType := mount.TypeVolume
			if strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") {
				mountType = mount.TypeBind
			}

			// Verify
			if mountType != tt.expectedType {
				t.Errorf("Mount type = %v, want %v", mountType, tt.expectedType)
			}
			if source != tt.expectedSource {
				t.Errorf("Source = %v, want %v", source, tt.expectedSource)
			}
			if target != tt.expectedTarget {
				t.Errorf("Target = %v, want %v", target, tt.expectedTarget)
			}
		})
	}
}
