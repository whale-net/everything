package apierrors

import (
	"testing"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Test that NewErrorDetail creates a valid ErrorDetail message
func TestNewErrorDetail(t *testing.T) {
	detail := NewErrorDetail(
		pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
		"device_id",
		"device_id",
		"INVALID_DEVICE_ID",
	)

	if detail == nil {
		t.Fatalf("NewErrorDetail returned nil")
	}
	if detail.FailureClass != pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT {
		t.Errorf("expected FAILURE_CLASS_INVALID_ARGUMENT, got %v", detail.FailureClass)
	}
	if detail.Entity != "device_id" {
		t.Errorf("expected entity 'device_id', got %q", detail.Entity)
	}
	if detail.Field != "device_id" {
		t.Errorf("expected field 'device_id', got %q", detail.Field)
	}
	if detail.MessageKey != "INVALID_DEVICE_ID" {
		t.Errorf("expected message key 'INVALID_DEVICE_ID', got %q", detail.MessageKey)
	}
}

// Test that ErrorDetailFromStatus extracts details from a gRPC status
func TestErrorDetailFromStatus_Extracts(t *testing.T) {
	detail := NewErrorDetail(
		pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
		"device_id",
		"device_id",
		InvalidDeviceID,
	)

	err := StatusWithDetail(codes.InvalidArgument, "device_id is required", detail)
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}

	// Extract the detail
	extracted := ErrorDetailFromStatus(st)
	if extracted == nil {
		t.Fatalf("expected ErrorDetail, got nil")
	}
	if extracted.FailureClass != detail.FailureClass {
		t.Errorf("FailureClass mismatch: %v != %v", extracted.FailureClass, detail.FailureClass)
	}
	if extracted.Entity != detail.Entity {
		t.Errorf("Entity mismatch: %q != %q", extracted.Entity, detail.Entity)
	}
	if extracted.Field != detail.Field {
		t.Errorf("Field mismatch: %q != %q", extracted.Field, detail.Field)
	}
	if extracted.MessageKey != detail.MessageKey {
		t.Errorf("MessageKey mismatch: %q != %q", extracted.MessageKey, detail.MessageKey)
	}
}

// Test that ErrorDetailFromStatus returns nil for a status without details
func TestErrorDetailFromStatus_NoDetails(t *testing.T) {
	// Create a status without details
	st := status.New(codes.Internal, "some error message")

	detail := ErrorDetailFromStatus(st)
	if detail != nil {
		t.Errorf("expected nil, got %v", detail)
	}
}

// Test that ErrorDetailFromStatus returns nil for nil status
func TestErrorDetailFromStatus_NilStatus(t *testing.T) {
	detail := ErrorDetailFromStatus(nil)
	if detail != nil {
		t.Errorf("expected nil, got %v", detail)
	}
}

// Test that StatusWithDetail creates a gRPC error with attached details
func TestStatusWithDetail_CreatesError(t *testing.T) {
	detail := NewErrorDetail(
		pb.FailureClass_FAILURE_CLASS_PRECONDITION,
		"board",
		"",
		"BOARD_NOT_FOUND",
	)

	err := StatusWithDetail(codes.FailedPrecondition, "board 123 not found", detail)

	if err == nil {
		t.Fatalf("StatusWithDetail returned nil")
	}

	// Extract status
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}

	// Verify code and message
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("expected codes.FailedPrecondition, got %v", st.Code())
	}
	if st.Message() != "board 123 not found" {
		t.Errorf("expected message 'board 123 not found', got %q", st.Message())
	}

	// Verify detail is attached
	extracted := ErrorDetailFromStatus(st)
	if extracted == nil {
		t.Fatalf("expected ErrorDetail in status, got nil")
	}
}

// Test that all failure classes are supported
func TestFailureClasses(t *testing.T) {
	tests := []struct {
		name  string
		class pb.FailureClass
	}{
		{"InvalidArgument", pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT},
		{"Precondition", pb.FailureClass_FAILURE_CLASS_PRECONDITION},
		{"Internal", pb.FailureClass_FAILURE_CLASS_INTERNAL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := NewErrorDetail(tt.class, "entity", "field", "MESSAGE_KEY")
			if detail.FailureClass != tt.class {
				t.Errorf("expected %v, got %v", tt.class, detail.FailureClass)
			}

			err := StatusWithDetail(codes.InvalidArgument, "test", detail)
			if err == nil {
				t.Fatalf("StatusWithDetail returned nil")
			}

			extracted := ErrorDetailFromStatus(status.Convert(err))
			if extracted == nil {
				t.Fatalf("expected ErrorDetail, got nil")
			}
			if extracted.FailureClass != tt.class {
				t.Errorf("extracted class mismatch: %v != %v", extracted.FailureClass, tt.class)
			}
		})
	}
}

// Test that message keys are defined for common failures
func TestMessageKeys(t *testing.T) {
	expectedKeys := []string{
		InvalidDeviceID,
		BoardNotFound,
		ConfigNotFound,
		InternalError,
		InvalidPageToken,
	}

	for _, key := range expectedKeys {
		if key == "" {
			t.Errorf("expected non-empty message key")
		}
		// Verify key is upper case with underscores (convention)
		for _, c := range key {
			if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
				t.Errorf("invalid message key %q (should be UPPER_SNAKE_CASE)", key)
				break
			}
		}
	}
}

// Test error domain constant
func TestErrorDomain(t *testing.T) {
	if ErrorDomain == "" {
		t.Errorf("expected non-empty ErrorDomain")
	}
	if ErrorDomain != "leaflab.whale-net" {
		t.Errorf("expected 'leaflab.whale-net', got %q", ErrorDomain)
	}
}
