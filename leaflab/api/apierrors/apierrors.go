// Package apierrors defines structured error handling for the LeafLab API.
//
// All failures are communicated as gRPC status details carrying an ErrorDetail
// message, allowing callers to classify failures without parsing prose. The
// server is responsible for setting failure_class, entity, field, and
// message_key on every error it returns. The UI layer uses message_key to
// render personalized prose.
//
// This package is intentionally small and separate from server implementation
// details, so client code (CLIs, test drivers) can import just this to read
// and assert on error structure.
package apierrors

import (
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrorDomain is the domain constant that appears on all ErrorDetail messages
// from the leaflab API. Clients use this to confirm they are reading a
// leaflab-format error before inspecting the detail's fields.
const ErrorDomain = "leaflab.whale-net"

// MessageKeys are the standardized keys used in ErrorDetail.message_key
// to allow the UI layer to render localized, user-facing prose without
// knowing proto field names, status codes, or implementation details.

const (
	// InvalidDeviceID is returned when device_id is empty, missing, or
	// contains invalid characters (MQTT/AMQP restrictions).
	InvalidDeviceID = "INVALID_DEVICE_ID"

	// BoardNotFound is returned when an operation references a board_id or
	// device_id that has never sent a manifest.
	BoardNotFound = "BOARD_NOT_FOUND"

	// ConfigNotFound is returned when GetDeviceConfig is called on a device
	// that has never had a config pushed to it.
	ConfigNotFound = "CONFIG_NOT_FOUND"

	// InternalError is returned when a database, MQTT, or system error occurs.
	InternalError = "INTERNAL_ERROR"

	// InvalidPageToken is returned when a page_token is malformed or foreign.
	InvalidPageToken = "INVALID_PAGE_TOKEN"
)

// ErrorDetailFromStatus attempts to extract the ErrorDetail from a gRPC
// status. Returns nil if the status has no ErrorDetail attached, or if the
// detail cannot be unmarshaled. Used by clients to inspect structured errors.
func ErrorDetailFromStatus(st *status.Status) *pb.ErrorDetail {
	if st == nil {
		return nil
	}
	for _, d := range st.Details() {
		if detail, ok := d.(*pb.ErrorDetail); ok {
			return detail
		}
	}
	return nil
}

// NewErrorDetail is a helper for constructing an ErrorDetail message with
// commonly-used defaults. Used by server/handlers to build consistent errors.
func NewErrorDetail(class pb.FailureClass, entity, field, messageKey string) *pb.ErrorDetail {
	return &pb.ErrorDetail{
		FailureClass: class,
		Entity:       entity,
		Field:        field,
		MessageKey:   messageKey,
	}
}

// StatusWithDetail constructs a gRPC status with an attached ErrorDetail.
// The message string is preserved for logging but is NOT intended for end-user
// display; use the message_key to render user-facing prose instead.
func StatusWithDetail(code codes.Code, message string, detail *pb.ErrorDetail) error {
	st := status.New(code, message)
	withDetail, err := st.WithDetails(detail)
	if err != nil {
		// WithDetails only errors on a malformed proto, which ErrorDetail
		// never is here — this error is deliberately ignored in favor of
		// the status without the detail as a fallback.
		return st.Err()
	}
	return withDetail.Err()
}
