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

	// ReasonRequired is returned when EnterElevation or RenewElevation is
	// called with an empty reason (FR10).
	ReasonRequired = "REASON_REQUIRED"

	// ReasonNotRestated is returned when RenewElevation is called with the
	// same reason as the elevation window it would extend (FR10): renewal
	// requires a freshly-stated reason, not a reuse.
	ReasonNotRestated = "REASON_NOT_RESTATED"

	// NoActiveElevation is returned when RenewElevation is called with no
	// active elevation against the target household to renew (FR10).
	NoActiveElevation = "NO_ACTIVE_ELEVATION"

	// AdminRoleRequired is returned when a non-admin principal calls an
	// admin-only RPC (the FR10 standing lane, elevation management).
	AdminRoleRequired = "ADMIN_ROLE_REQUIRED"

	// HouseholdNotFound is returned when an operation references a
	// household_id that does not exist.
	HouseholdNotFound = "HOUSEHOLD_NOT_FOUND"

	// ResolveNotFound is the single, generic failure returned by the FR10.2
	// standing lane's Resolve RPC for every case that must be
	// indistinguishable per NFR2: an unknown, expired or revoked support
	// reference (FR80), an unknown person, an unmatched or ambiguous
	// device-id prefix. Never branch client-visible behavior on which of
	// these occurred.
	ResolveNotFound = "RESOLVE_NOT_FOUND"

	// NoHousehold is returned when an authenticated principal has no
	// household membership and the operation requires one.
	NoHousehold = "NO_HOUSEHOLD"

	// SupportReferenceNotFound is returned by RevokeSupportReference when no
	// matching active reference exists for the caller's household. Unlike
	// ResolveNotFound, this is the household member managing their own
	// reference, not an admin-facing existence oracle — indistinguishability
	// does not apply here.
	SupportReferenceNotFound = "SUPPORT_REFERENCE_NOT_FOUND"
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
