package repository

import (
	"time"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// Status is the shared reconciliation lifecycle for App and Chart rows. See
// ARCHITECTURE.md "Data model" and AppStatus in protos/messages.proto.
type Status string

const (
	StatusActive   Status = "active"
	StatusMissing  Status = "missing"
	StatusArchived Status = "archived"
)

// ArtifactKind mirrors ArtifactKind in protos/messages.proto.
type ArtifactKind string

const (
	ArtifactKindImage ArtifactKind = "image"
	ArtifactKindChart ArtifactKind = "chart"
)

// Promotability mirrors Promotability in protos/messages.proto. Always
// derived server-side from the owning app's/chart's DeployUnit — never
// stored. See ARCHITECTURE.md "Promotability".
type Promotability string

const (
	PromotabilityPromotable    Promotability = "promotable"
	PromotabilityViaChart      Promotability = "via_chart"
	PromotabilityNotPromotable Promotability = "not_promotable"
)

// App mirrors one release_app manifest. Never hard-deleted.
type App struct {
	AppID           string
	Domain          string
	Name            string
	Description     string
	Language        string
	AppType         string
	DeployUnit      appmetapb.DeployUnit
	BazelLabel      string
	ImageRepository string
	Status          Status
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
}

func (a App) FullName() string { return a.Domain + "-" + a.Name }

// Chart is a Helm chart composing one or more apps.
type Chart struct {
	ChartID         string
	Domain          string
	Name            string
	Description     string
	ChartRepository string
	DeployUnit      appmetapb.DeployUnit
	Status          Status
	AppIDs          []string
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
}

func (c Chart) FullName() string { return c.Domain + "-" + c.Name }

// ReconcileResult buckets every App/Chart row touched by one ReconcileApps
// call, matching ReconcileAppsResponse in protos/api_messages_app.proto.
type ReconcileResult struct {
	CreatedApps        []App
	UpdatedApps        []App
	NewlyMissingApps   []App
	RecoveredApps      []App
	CreatedCharts      []Chart
	UpdatedCharts      []Chart
	NewlyMissingCharts []Chart
	RecoveredCharts    []Chart
}

// AppListFilter is ListAppsRequest's filter set, decoupled from the proto.
type AppListFilter struct {
	Domain     string
	Statuses   []Status // empty means [Active, Missing], the RPC default
	DeployUnit appmetapb.DeployUnit
}

// ChartListFilter is ListChartsRequest's filter set.
type ChartListFilter struct {
	Domain   string
	Statuses []Status
}

// Build is one CI run. Every artifact hangs off a build.
type Build struct {
	BuildID         string
	GitSHA          string
	GitRef          string
	WorkflowRunID   string
	WorkflowAttempt int32
	Actor           string
	StartedAt       *time.Time
	RecordedAt      time.Time
}

// ArtifactLink records that a chart artifact pins a specific image digest.
type ArtifactLink struct {
	ImageArtifactID string
	AppID           string
	Repository      string
	Version         string
	Digest          string
}

// Artifact is a published, digest-addressable image or chart.
type Artifact struct {
	ArtifactID  string
	Kind        ArtifactKind
	AppID       string // set when Kind == ArtifactKindImage
	ChartID     string // set when Kind == ArtifactKindChart
	Repository  string
	Version     string
	Digest      string
	BuildID     string
	PublishedAt time.Time

	// Promotability is DERIVED — computed at read time from the owner's
	// DeployUnit, never persisted. Populated by repository read methods.
	Promotability Promotability

	// Contains is populated only for Kind == ArtifactKindChart.
	Contains []ArtifactLink
}

// ContainedImageInput is one image a chart pins, as resolved by tools/helm.
type ContainedImageInput struct {
	AppFullName string
	Repository  string
	Version     string
	Digest      string
}

// ArtifactListFilter is ListArtifactsRequest's filter set.
type ArtifactListFilter struct {
	OwnerFullName  string
	Kind           ArtifactKind
	PromotableOnly bool
}

// ArtifactLookup identifies an artifact by exactly one of the supported
// keys, matching GetArtifactRequest's oneof-by-convention shape.
type ArtifactLookup struct {
	ArtifactID    string
	Digest        string
	OwnerFullName string
	Kind          ArtifactKind
	Version       string
}

// Environment is a promotion target, e.g. "dev"/"stage"/"prod". A row, not
// an enum, so ephemeral/regional environments are an insert, not a release
// — see ARCHITECTURE.md "Data model". Key is the immutable identity;
// Upsert may change every other field but never Key itself.
type Environment struct {
	EnvironmentID     string
	Key               string
	DisplayName       string
	Rank              int32
	RequiresApproval  bool
	GitopsPath        string
	AllowedPrincipals []string
	Archived          bool
	CreatedAt         time.Time
}

// PromotionState mirrors PromotionState in protos/messages.proto. AR-3c
// only ever writes PromotionStateActive; the other values are reserved for
// the approval gate described in ARCHITECTURE.md "Future: approval gate".
type PromotionState string

const (
	PromotionStatePendingApproval PromotionState = "pending_approval"
	PromotionStateActive          PromotionState = "active"
	PromotionStateSuperseded      PromotionState = "superseded"
	PromotionStateFailed          PromotionState = "failed"
)

// PromotionAction mirrors PromotionAction in protos/messages.proto — the
// human-meaningful verb recorded on promotion_event.
type PromotionAction string

const (
	PromotionActionPromote  PromotionAction = "promote"
	PromotionActionRollback PromotionAction = "rollback"
	PromotionActionOverride PromotionAction = "override"
	PromotionActionRetire   PromotionAction = "retire"
	PromotionActionApprove  PromotionAction = "approve"
	PromotionActionReject   PromotionAction = "reject"
)

// TargetKey is the promoted thing's identity, denormalized onto the
// promotion table so its partial unique index can be expressed without a
// nullable two-column target (app_id is NULL for chart artifacts and vice
// versa) — see ARCHITECTURE.md "SCD2 on promotion".
func TargetKey(kind ArtifactKind, ownerFullName string) string {
	return string(kind) + ":" + ownerFullName
}

// Promotion is SCD2 state: what is deployed to an environment right now, or
// at any past instant. Follows the repo-wide valid_from/valid_to convention
// — see AGENTS.md "SCD2". AppID/ChartID/Repository/Version/Digest are
// populated by a join to the promoted artifact at read time — never stored
// redundantly on the promotion row itself, so there is exactly one place
// that data can drift from. ValidTo == nil means still current.
type Promotion struct {
	PromotionID    string
	EnvironmentID  string
	EnvironmentKey string // denormalized at read time, for readability
	TargetKey      string

	Kind       ArtifactKind
	AppID      string // set when Kind == ArtifactKindImage
	ChartID    string // set when Kind == ArtifactKindChart
	ArtifactID string
	Repository string
	Version    string
	Digest     string

	State      PromotionState
	IsOverride bool

	ValidFrom time.Time
	ValidTo   *time.Time
}

// PromotionEvent is the append-only audit log. NOT SCD2 — see AGENTS.md,
// event logs get their own shape.
type PromotionEvent struct {
	EventID            string
	PromotionID        string
	Action             PromotionAction
	Actor              string
	Reason             string
	TemporalWorkflowID string
	TemporalRunID      string
	OccurredAt         time.Time
}

// PromotionListFilter is ListPromotionsRequest's filter set.
type PromotionListFilter struct {
	EnvironmentKey string
	OwnerFullName  string
	IncludeHistory bool
}

// PromotionEventListFilter is ListPromotionEventsRequest's filter set.
type PromotionEventListFilter struct {
	PromotionID    string
	EnvironmentKey string
	OwnerFullName  string
	Actor          string
	Since          time.Time
}
