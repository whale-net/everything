package handlers

import (
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

func unixToTime(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func timeToUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func appStatusToPB(s repository.Status) pb.AppStatus {
	switch s {
	case repository.StatusActive:
		return pb.AppStatus_APP_STATUS_ACTIVE
	case repository.StatusMissing:
		return pb.AppStatus_APP_STATUS_MISSING
	case repository.StatusArchived:
		return pb.AppStatus_APP_STATUS_ARCHIVED
	default:
		return pb.AppStatus_APP_STATUS_UNSPECIFIED
	}
}

func appStatusFromPB(s pb.AppStatus) repository.Status {
	switch s {
	case pb.AppStatus_APP_STATUS_ACTIVE:
		return repository.StatusActive
	case pb.AppStatus_APP_STATUS_MISSING:
		return repository.StatusMissing
	case pb.AppStatus_APP_STATUS_ARCHIVED:
		return repository.StatusArchived
	default:
		return ""
	}
}

func artifactKindToPB(k repository.ArtifactKind) pb.ArtifactKind {
	switch k {
	case repository.ArtifactKindImage:
		return pb.ArtifactKind_ARTIFACT_KIND_IMAGE
	case repository.ArtifactKindChart:
		return pb.ArtifactKind_ARTIFACT_KIND_CHART
	default:
		return pb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED
	}
}

func artifactKindFromPB(k pb.ArtifactKind) repository.ArtifactKind {
	switch k {
	case pb.ArtifactKind_ARTIFACT_KIND_IMAGE:
		return repository.ArtifactKindImage
	case pb.ArtifactKind_ARTIFACT_KIND_CHART:
		return repository.ArtifactKindChart
	default:
		return ""
	}
}

func promotabilityToPB(p repository.Promotability) pb.Promotability {
	switch p {
	case repository.PromotabilityPromotable:
		return pb.Promotability_PROMOTABILITY_PROMOTABLE
	case repository.PromotabilityViaChart:
		return pb.Promotability_PROMOTABILITY_VIA_CHART
	case repository.PromotabilityNotPromotable:
		return pb.Promotability_PROMOTABILITY_NOT_PROMOTABLE
	default:
		return pb.Promotability_PROMOTABILITY_UNSPECIFIED
	}
}

func appToPB(a repository.App) *pb.App {
	return &pb.App{
		AppId:           a.AppID,
		Domain:          a.Domain,
		Name:            a.Name,
		FullName:        a.FullName(),
		Description:     a.Description,
		Language:        a.Language,
		AppType:         a.AppType,
		DeployUnit:      a.DeployUnit,
		BazelLabel:      a.BazelLabel,
		ImageRepository: a.ImageRepository,
		Status:          appStatusToPB(a.Status),
		FirstSeenAt:     timeToUnix(a.FirstSeenAt),
		LastSeenAt:      timeToUnix(a.LastSeenAt),
	}
}

func appsToPB(apps []repository.App) []*pb.App {
	out := make([]*pb.App, 0, len(apps))
	for _, a := range apps {
		out = append(out, appToPB(a))
	}
	return out
}

func chartToPB(c repository.Chart) *pb.Chart {
	return &pb.Chart{
		ChartId:         c.ChartID,
		Domain:          c.Domain,
		Name:            c.Name,
		FullName:        c.FullName(),
		Description:     c.Description,
		AppIds:          c.AppIDs,
		ChartRepository: c.ChartRepository,
		DeployUnit:      c.DeployUnit,
		Status:          appStatusToPB(c.Status),
		FirstSeenAt:     timeToUnix(c.FirstSeenAt),
		LastSeenAt:      timeToUnix(c.LastSeenAt),
	}
}

func chartsToPB(charts []repository.Chart) []*pb.Chart {
	out := make([]*pb.Chart, 0, len(charts))
	for _, c := range charts {
		out = append(out, chartToPB(c))
	}
	return out
}

func buildToPB(b repository.Build) *pb.Build {
	var startedAt int64
	if b.StartedAt != nil {
		startedAt = timeToUnix(*b.StartedAt)
	}
	return &pb.Build{
		BuildId:         b.BuildID,
		GitSha:          b.GitSHA,
		GitRef:          b.GitRef,
		WorkflowRunId:   b.WorkflowRunID,
		WorkflowAttempt: b.WorkflowAttempt,
		Actor:           b.Actor,
		StartedAt:       startedAt,
		RecordedAt:      timeToUnix(b.RecordedAt),
	}
}

func buildsToPB(builds []repository.Build) []*pb.Build {
	out := make([]*pb.Build, 0, len(builds))
	for _, b := range builds {
		out = append(out, buildToPB(b))
	}
	return out
}

func artifactLinkToPB(l repository.ArtifactLink) *pb.ArtifactLink {
	return &pb.ArtifactLink{
		ArtifactId: l.ImageArtifactID,
		AppId:      l.AppID,
		Repository: l.Repository,
		Version:    l.Version,
		Digest:     l.Digest,
	}
}

func artifactToPB(a repository.Artifact) *pb.Artifact {
	out := &pb.Artifact{
		ArtifactId:    a.ArtifactID,
		Kind:          artifactKindToPB(a.Kind),
		Repository:    a.Repository,
		Version:       a.Version,
		Digest:        a.Digest,
		BuildId:       a.BuildID,
		PublishedAt:   timeToUnix(a.PublishedAt),
		Promotability: promotabilityToPB(a.Promotability),
	}
	if a.Kind == repository.ArtifactKindImage {
		out.AppId = a.AppID
	} else {
		out.ChartId = a.ChartID
	}
	for _, l := range a.Contains {
		out.Contains = append(out.Contains, artifactLinkToPB(l))
	}
	return out
}

func artifactsToPB(artifacts []repository.Artifact) []*pb.Artifact {
	out := make([]*pb.Artifact, 0, len(artifacts))
	for _, a := range artifacts {
		out = append(out, artifactToPB(a))
	}
	return out
}

func appStatusesFromPB(statuses []pb.AppStatus) []repository.Status {
	out := make([]repository.Status, 0, len(statuses))
	for _, s := range statuses {
		if v := appStatusFromPB(s); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func environmentToPB(e repository.Environment) *pb.Environment {
	return &pb.Environment{
		EnvironmentId:     e.EnvironmentID,
		Key:               e.Key,
		DisplayName:       e.DisplayName,
		Rank:              e.Rank,
		RequiresApproval:  e.RequiresApproval,
		GitopsPath:        e.GitopsPath,
		AllowedPrincipals: e.AllowedPrincipals,
		Archived:          e.Archived,
		CreatedAt:         timeToUnix(e.CreatedAt),
	}
}

func environmentsToPB(envs []repository.Environment) []*pb.Environment {
	out := make([]*pb.Environment, 0, len(envs))
	for _, e := range envs {
		out = append(out, environmentToPB(e))
	}
	return out
}

func promotionStateToPB(s repository.PromotionState) pb.PromotionState {
	switch s {
	case repository.PromotionStatePendingApproval:
		return pb.PromotionState_PROMOTION_STATE_PENDING_APPROVAL
	case repository.PromotionStateActive:
		return pb.PromotionState_PROMOTION_STATE_ACTIVE
	case repository.PromotionStateSuperseded:
		return pb.PromotionState_PROMOTION_STATE_SUPERSEDED
	case repository.PromotionStateFailed:
		return pb.PromotionState_PROMOTION_STATE_FAILED
	default:
		return pb.PromotionState_PROMOTION_STATE_UNSPECIFIED
	}
}

func promotionActionToPB(a repository.PromotionAction) pb.PromotionAction {
	switch a {
	case repository.PromotionActionPromote:
		return pb.PromotionAction_PROMOTION_ACTION_PROMOTE
	case repository.PromotionActionRollback:
		return pb.PromotionAction_PROMOTION_ACTION_ROLLBACK
	case repository.PromotionActionOverride:
		return pb.PromotionAction_PROMOTION_ACTION_OVERRIDE
	case repository.PromotionActionRetire:
		return pb.PromotionAction_PROMOTION_ACTION_RETIRE
	case repository.PromotionActionApprove:
		return pb.PromotionAction_PROMOTION_ACTION_APPROVE
	case repository.PromotionActionReject:
		return pb.PromotionAction_PROMOTION_ACTION_REJECT
	default:
		return pb.PromotionAction_PROMOTION_ACTION_UNSPECIFIED
	}
}

func promotionToPB(p repository.Promotion) *pb.Promotion {
	out := &pb.Promotion{
		PromotionId:    p.PromotionID,
		EnvironmentId:  p.EnvironmentID,
		EnvironmentKey: p.EnvironmentKey,
		ArtifactId:     p.ArtifactID,
		Repository:     p.Repository,
		Version:        p.Version,
		Digest:         p.Digest,
		State:          promotionStateToPB(p.State),
		IsOverride:     p.IsOverride,
		ValidFrom:      timeToUnix(p.ValidFrom),
	}
	if p.Kind == repository.ArtifactKindImage {
		out.AppId = p.AppID
	} else {
		out.ChartId = p.ChartID
	}
	if p.ValidTo != nil {
		out.ValidTo = timeToUnix(*p.ValidTo)
	}
	return out
}

func promotionsToPB(promotions []repository.Promotion) []*pb.Promotion {
	out := make([]*pb.Promotion, 0, len(promotions))
	for _, p := range promotions {
		out = append(out, promotionToPB(p))
	}
	return out
}

func promotionEventToPB(e repository.PromotionEvent) *pb.PromotionEvent {
	return &pb.PromotionEvent{
		EventId:            e.EventID,
		PromotionId:        e.PromotionID,
		Action:             promotionActionToPB(e.Action),
		Actor:              e.Actor,
		Reason:             e.Reason,
		TemporalWorkflowId: e.TemporalWorkflowID,
		TemporalRunId:      e.TemporalRunID,
		OccurredAt:         timeToUnix(e.OccurredAt),
	}
}

func promotionEventsToPB(events []repository.PromotionEvent) []*pb.PromotionEvent {
	out := make([]*pb.PromotionEvent, 0, len(events))
	for _, e := range events {
		out = append(out, promotionEventToPB(e))
	}
	return out
}

func containedImagesFromPB(images []*pb.ContainedImage) []repository.ContainedImageInput {
	out := make([]repository.ContainedImageInput, 0, len(images))
	for _, ci := range images {
		out = append(out, repository.ContainedImageInput{
			AppFullName: ci.AppFullName,
			Repository:  ci.Repository,
			Version:     ci.Version,
			Digest:      ci.Digest,
		})
	}
	return out
}
