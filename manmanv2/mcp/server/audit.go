package server

import (
	"context"
	"log/slog"
)

// Audit outcomes.
const (
	OutcomeAllowed = "allowed"
	OutcomeRefused = "refused"
	OutcomeError   = "error"
)

// AuditRecord is one structured per-call audit entry.
type AuditRecord struct {
	Subject  string
	Persona  Persona
	Tool     string
	TargetID string
	Outcome  string
	// Reason is the refusal or failure cause, if any.
	Reason string
	// Snapshot is the pre-call state captured by Tool.Snapshot, if any.
	Snapshot any
}

// Auditor receives one record per tool call, allowed or refused.
type Auditor interface {
	Record(ctx context.Context, r AuditRecord)
}

// LogAuditor writes records as structured slog entries: INFO for allowed and
// expected refusals, ERROR only for failures.
type LogAuditor struct{ Logger *slog.Logger }

func (a LogAuditor) Record(ctx context.Context, r AuditRecord) {
	attrs := []any{
		"subject", r.Subject,
		"persona", r.Persona.String(),
		"tool", r.Tool,
		"target_id", r.TargetID,
		"outcome", r.Outcome,
	}
	if r.Reason != "" {
		attrs = append(attrs, "reason", r.Reason)
	}
	if r.Snapshot != nil {
		attrs = append(attrs, "snapshot", r.Snapshot)
	}
	if r.Outcome == OutcomeError {
		a.Logger.ErrorContext(ctx, "mcp tool call", attrs...)
		return
	}
	a.Logger.InfoContext(ctx, "mcp tool call", attrs...)
}
