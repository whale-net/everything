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
