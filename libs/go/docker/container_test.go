package docker

import (
	"strings"
	"testing"
	"time"

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

func TestSplitLogTimestamp(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		expectOK    bool
		expectRest  string
		expectedTS  string // RFC3339Nano, only checked when expectOK
	}{
		{
			name:       "well-formed docker timestamp prefix",
			line:       "2024-01-15T10:30:00.123456789Z hello world",
			expectOK:   true,
			expectRest: "hello world",
			expectedTS: "2024-01-15T10:30:00.123456789Z",
		},
		{
			name:       "empty message after timestamp",
			line:       "2024-01-15T10:30:00.000000000Z ",
			expectOK:   true,
			expectRest: "",
			expectedTS: "2024-01-15T10:30:00.000000000Z",
		},
		{
			name:       "no timestamp prefix at all",
			line:       "hello world",
			expectOK:   false,
			expectRest: "hello world",
		},
		{
			name:       "too short to contain a timestamp",
			line:       "short",
			expectOK:   false,
			expectRest: "short",
		},
		{
			// Right length and a space in the right place, but not a valid
			// RFC3339Nano timestamp — exercises the time.Parse failure path.
			name:       "correct width and spacing but unparseable timestamp",
			line:       strings.Repeat("x", len("2006-01-02T15:04:05.000000000Z")) + " message",
			expectOK:   false,
			expectRest: strings.Repeat("x", len("2006-01-02T15:04:05.000000000Z")) + " message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, rest, ok := SplitLogTimestamp(tt.line)
			if ok != tt.expectOK {
				t.Fatalf("ok = %v, want %v", ok, tt.expectOK)
			}
			if rest != tt.expectRest {
				t.Errorf("rest = %q, want %q", rest, tt.expectRest)
			}
			if tt.expectOK {
				want, err := time.Parse(time.RFC3339Nano, tt.expectedTS)
				if err != nil {
					t.Fatalf("bad test fixture: %v", err)
				}
				if !ts.Equal(want) {
					t.Errorf("ts = %v, want %v", ts, want)
				}
			}
		})
	}
}
