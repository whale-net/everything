package store

import (
	"time"

	"github.com/google/uuid"
)

// Person is one row of `person` (migration 001) -- one per authenticated
// human, keyed on the Google OAuth `sub` claim (FR1/FR2), not email.
type Person struct {
	ID            uuid.UUID
	GoogleSubject string
	Email         string
	DisplayName   string
	CreatedAt     time.Time
}

// ConnectionState is `channel.connection_state` (migration 001, FR4).
type ConnectionState string

const (
	ConnectionStateConnected   ConnectionState = "connected"
	ConnectionStateNeedsReauth ConnectionState = "needs_reauth"
)

// Channel is one row of `channel` (migration 001) -- one per connected
// YouTube channel. Deliberately has no owner field: ownership, and every
// other role, lives only in channel_person (LB2) -- see RoleStore and
// authz.go.
type Channel struct {
	ID                       uuid.UUID
	YouTubeChannelID         string
	Title                    string
	ConnectionState          ConnectionState
	ConnectionStateChangedAt time.Time
	CreatedAt                time.Time
}

// Role is `channel_person.role` (migration 001, LB2). M1 only ever
// populates RoleCreator and RoleAnalyst rows, but nothing in this package
// assumes those are the only two roles that will ever exist.
type Role string

const (
	RoleCreator Role = "creator"
	RoleAnalyst Role = "analyst"
)

// ChannelPerson is one row of `channel_person` -- the LB2 join table,
// SCD2 per AGENTS.md "SCD2". ValidTo == nil means this role is currently
// held; a non-nil ValidTo means it was closed (superseded or revoked) at
// that time.
type ChannelPerson struct {
	ID        uuid.UUID
	ChannelID uuid.UUID
	PersonID  uuid.UUID
	Role      Role
	ValidFrom time.Time
	ValidTo   *time.Time
}

// Invite is one row of `channel_invite` (migration 001, FR5-FR8) -- a
// single-use, high-entropy code a Channel's creator generates to let
// another Person accept an analyst role.
type Invite struct {
	ID                 uuid.UUID
	ChannelID          uuid.UUID
	Code               string
	CreatedByPersonID  uuid.UUID
	CreatedAt          time.Time
	ConsumedAt         *time.Time
	ConsumedByPersonID *uuid.UUID
	InvalidatedAt      *time.Time
}
