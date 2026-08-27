package main

import (
	"context"
	"os"
	"strings"

	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc"
)

// ExposureAllowlistEnvVar names the env var this gate reads (A30, Phase 1
// exit criterion 7): a comma-separated list of authenticated principal
// subjects (grpcauth.Claims.Subject) permitted to reach any RPC beyond
// GetHealth. Empty, unset, or containing only blank/whitespace entries
// means nobody is admitted -- fail-closed by construction (missing
// configuration refuses everyone), not by an operator remembering to set
// it correctly.
//
// Implementation's mechanism choice (A30 lists three acceptable options: a
// principal allowlist, a feature gate defaulting closed, or a
// non-production-only deployment): a principal allowlist. It is the
// smallest surface area of the three -- one env var, one interceptor pair,
// no chart/release-config coupling -- and it is exercised the same way in
// every environment (dev, staging, prod all read the same gate), so there
// is no separate "non-prod deploy" code path to keep correct. A feature
// gate would need the same fail-closed-default discipline for no real
// benefit over an allowlist here, since Phase 1 has no notion of a
// per-user feature yet.
//
// TODO(FR5/NFR1.b): this entire gate -- this file, its BUILD.bazel entry,
// and every call site wired to it -- is removed in #1339, the Phase 2 task
// that lands per-entity household authorization (FR4, FR5, FR1.1, NFR2)
// and makes this API safe to expose to real users on its own scoping.
// Deleting the gate in one change is the "removable in one identifiable
// change" A30 requires.
const ExposureAllowlistEnvVar = "LEAFLAB_API_EXPOSURE_ALLOWLIST"

// ParseExposureAllowlist splits raw (ExposureAllowlistEnvVar's value) on
// commas into a set of allowed subjects. Whitespace around each entry is
// trimmed; empty entries are dropped. An empty or all-blank raw value
// produces an empty (non-nil) set -- callers must treat an empty set as
// "admit nobody", never as "no restriction" (fail-closed).
func ParseExposureAllowlist(raw string) map[string]struct{} {
	allowlist := make(map[string]struct{})
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		allowlist[entry] = struct{}{}
	}
	return allowlist
}

// LoadExposureAllowlistFromEnv reads ExposureAllowlistEnvVar via os.Getenv
// and parses it with ParseExposureAllowlist. Extracted so a future
// Implementation-phase run() stays a thin wiring function and this parsing
// is unit-testable without touching the process environment.
func LoadExposureAllowlistFromEnv() map[string]struct{} {
	return ParseExposureAllowlist(os.Getenv(ExposureAllowlistEnvVar))
}

// NewExposureUnaryInterceptor enforces A30's Phase 1 non-exposure gate: any
// authenticated principal (grpcauth.Claims.Subject) not present in
// allowlist is refused with a FailurePermissionDenied (FR59) on every RPC
// except the anonymous allowlist (GetHealth -- see auth.go's
// anonymousMethods, reused here rather than re-derived). An empty
// allowlist refuses everyone (fail-closed).
//
// Wired into main.go's buildServer chain immediately after
// NewAuthEnforcementUnaryInterceptor, so Claims are already known-present
// by the time this interceptor reads Subject. Implementation picked the
// principal allowlist as the enforcement mechanism (over a feature gate or
// a non-production-only deployment) -- see this file's package doc comment
// on ExposureAllowlistEnvVar above for why.
func NewExposureUnaryInterceptor(allowlist map[string]struct{}) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if anonymousMethods[info.FullMethod] {
			return handler(ctx, req)
		}
		if !exposureAllows(ctx, allowlist) {
			return nil, exposureRefusal()
		}
		return handler(ctx, req)
	}
}

// NewExposureStreamInterceptor is the streaming counterpart of
// NewExposureUnaryInterceptor. See that function's doc comment.
func NewExposureStreamInterceptor(allowlist map[string]struct{}) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if anonymousMethods[info.FullMethod] {
			return handler(srv, ss)
		}
		if !exposureAllows(ss.Context(), allowlist) {
			return exposureRefusal()
		}
		return handler(srv, ss)
	}
}

// exposureAllows reports whether ctx's Claims carry a Subject present in
// allowlist. A missing Claims (should not happen this late in the chain --
// NewAuthEnforcementUnaryInterceptor already refused it) is treated as not
// allowed, never a panic.
func exposureAllows(ctx context.Context, allowlist map[string]struct{}) bool {
	claims, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok || claims == nil {
		return false
	}
	_, allowed := allowlist[claims.Subject]
	return allowed
}

// exposureRefusal builds the FR59 permission_denied failure a refused
// caller sees. The reason is plainly worded (FR59.2) -- no mention of
// allowlists, environment variables, or any other internal mechanism.
func exposureRefusal() error {
	return contract.PermissionDenied("request", "", "This isn't open yet.")
}
