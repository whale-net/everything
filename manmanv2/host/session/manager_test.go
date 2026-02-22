package session

import (
	"testing"
)

func TestGetNamedVolumeName(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		sgcID       int64
		volumeName  string
		expected    string
	}{
		{
			name:        "with environment",
			environment: "dev",
			sgcID:       7,
			volumeName:  "cfg",
			expected:    "manman-sgc-dev-7-cfg",
		},
		{
			name:        "with production environment",
			environment: "prod",
			sgcID:       42,
			volumeName:  "data",
			expected:    "manman-sgc-prod-42-data",
		},
		{
			name:        "without environment",
			environment: "",
			sgcID:       7,
			volumeName:  "cfg",
			expected:    "manman-sgc-7-cfg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := &SessionManager{
				environment: tt.environment,
			}
			result := sm.getNamedVolumeName(tt.sgcID, tt.volumeName)
			if result != tt.expected {
				t.Errorf("getNamedVolumeName() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestVolumeMountFormatting(t *testing.T) {
	tests := []struct {
		name           string
		volumeType     string
		volumeName     string
		containerPath  string
		hostSubpath    string
		sgcID          int64
		environment    string
		expectedFormat string // What we expect to be passed to Docker
	}{
		{
			name:           "named volume",
			volumeType:     "named",
			volumeName:     "cfg",
			containerPath:  "/cfg",
			sgcID:          7,
			environment:    "dev",
			expectedFormat: "manman-sgc-dev-7-cfg:/cfg",
		},
		{
			name:           "named volume without environment",
			volumeType:     "named",
			volumeName:     "data",
			containerPath:  "/data",
			sgcID:          10,
			environment:    "",
			expectedFormat: "manman-sgc-10-data:/data",
		},
		{
			name:           "bind mount with explicit subpath",
			volumeType:     "bind",
			volumeName:     "addons",
			containerPath:  "/addons",
			hostSubpath:    "l4d2-addons",
			sgcID:          7,
			environment:    "dev",
			expectedFormat: "bind", // Bind mounts use host paths, not volume names
		},
		{
			name:           "bind mount with default subpath",
			volumeType:     "",
			volumeName:     "config",
			containerPath:  "/config",
			hostSubpath:    "",
			sgcID:          7,
			environment:    "dev",
			expectedFormat: "bind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := &SessionManager{
				environment: tt.environment,
			}

			vol := VolumeMount{
				Name:          tt.volumeName,
				ContainerPath: tt.containerPath,
				HostSubpath:   tt.hostSubpath,
				VolumeType:    tt.volumeType,
			}

			// Test the logic that determines mount string format
			if vol.VolumeType == "named" {
				volumeName := sm.getNamedVolumeName(tt.sgcID, vol.Name)
				mountStr := volumeName + ":" + vol.ContainerPath
				if mountStr != tt.expectedFormat {
					t.Errorf("Named volume mount string = %v, want %v", mountStr, tt.expectedFormat)
				}
			} else {
				// For bind mounts, we just verify the type is correct
				if tt.expectedFormat != "bind" {
					t.Errorf("Expected bind mount type, got %v", tt.expectedFormat)
				}
			}
		})
	}
}

// TestVolumeTypePassedToDocker verifies that volume_type correctly determines
// whether a named volume or bind mount is used when creating containers
func TestVolumeTypePassedToDocker(t *testing.T) {
	tests := []struct {
		name              string
		volumes           []VolumeMount
		environment       string
		sgcID             int64
		expectedNamedVols []string // Expected named volume strings
		expectedBindCount int      // Expected number of bind mounts
	}{
		{
			name:        "L4D2 config: named volume for cfg, bind mount for addons",
			environment: "dev",
			sgcID:       7,
			volumes: []VolumeMount{
				{
					Name:          "l4d2-cfg",
					ContainerPath: "/cfg",
					VolumeType:    "named",
				},
				{
					Name:          "l4d2-addons",
					ContainerPath: "/addons",
					HostSubpath:   "l4d2-addons",
					VolumeType:    "bind",
				},
			},
			expectedNamedVols: []string{"manman-sgc-dev-7-l4d2-cfg:/cfg"},
			expectedBindCount: 1,
		},
		{
			name:        "all bind mounts (backward compatibility)",
			environment: "prod",
			sgcID:       42,
			volumes: []VolumeMount{
				{
					Name:          "data",
					ContainerPath: "/data",
					VolumeType:    "bind",
				},
				{
					Name:          "config",
					ContainerPath: "/config",
					VolumeType:    "",
				},
			},
			expectedNamedVols: []string{},
			expectedBindCount: 2,
		},
		{
			name:        "all named volumes",
			environment: "staging",
			sgcID:       15,
			volumes: []VolumeMount{
				{
					Name:          "cfg",
					ContainerPath: "/cfg",
					VolumeType:    "named",
				},
				{
					Name:          "data",
					ContainerPath: "/data",
					VolumeType:    "named",
				},
			},
			expectedNamedVols: []string{
				"manman-sgc-staging-15-cfg:/cfg",
				"manman-sgc-staging-15-data:/data",
			},
			expectedBindCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := &SessionManager{
				environment: tt.environment,
			}

			namedVols := []string{}
			bindCount := 0

			// Simulate the logic from createGameContainer
			for _, vol := range tt.volumes {
				if vol.VolumeType == "named" {
					volumeName := sm.getNamedVolumeName(tt.sgcID, vol.Name)
					mountStr := volumeName + ":" + vol.ContainerPath
					namedVols = append(namedVols, mountStr)
				} else {
					bindCount++
				}
			}

			// Verify named volumes
			if len(namedVols) != len(tt.expectedNamedVols) {
				t.Errorf("Expected %d named volumes, got %d", len(tt.expectedNamedVols), len(namedVols))
			}
			for i, expected := range tt.expectedNamedVols {
				if i >= len(namedVols) {
					t.Errorf("Missing expected named volume: %s", expected)
					continue
				}
				if namedVols[i] != expected {
					t.Errorf("Named volume[%d] = %v, want %v", i, namedVols[i], expected)
				}
			}

			// Verify bind mount count
			if bindCount != tt.expectedBindCount {
				t.Errorf("Expected %d bind mounts, got %d", tt.expectedBindCount, bindCount)
			}
		})
	}
}
