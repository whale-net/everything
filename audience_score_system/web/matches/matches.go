// Package matches is `web`'s Channel-scoped pending-matches browse page
// (milestone M4.3, capability C9): serves GET /channels/{id}/matches, the
// `web` mirror of `list_pending_matches`
// (audience_score_system/mcp/tools/matches.go). Handlers and templ views
// live together in this one package, mirroring web/research's (which
// itself mirrors web/schedule's) package doc comment rationale: the read
// flow and its views are tightly coupled with no reuse outside this
// package.
//
// Read-only in this issue (#1926, FR7/FR8/NFR2, plus the "Resolve pending
// matches" link half of FR11) -- the confirm/reject forms land in the
// follow-up task (FR9/FR10), which depends on this one. Per the plan's
// Out of scope, this package adds no new store method, migration, or
// schema change: every read it needs (store.MatchStore.ListPending plus
// the same store.SyncStore/store.VideoScriptStore/store.VerdictStore
// enrichment mcp/tools/matches.go's renderPendingMatch already performs)
// already exists.
//
// renderMatchVideo/renderMatchScript below deliberately reproduce
// mcp/tools/matches.go's identically-named functions against the same
// stores (LB5: no parallel query, no new store method) rather than
// importing them -- they are unexported in package tools, and the two
// surfaces (MCP JSON output vs. templ view) render different shapes from
// the same read, so a shared exported helper would need to serve both
// callers' output types anyway.
//
// Routes (mounted by ../main.go's setupRoutes, behind
// web/auth.Authenticator.RequireSignedIn):
//
//   - GET /channels/{id}/matches -- HandleList (FR7, FR8).
package matches

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

// defaultPendingLimit bounds HandleList's response to the same fixed page
// size as defaultListPendingMatchesLimit (mcp/tools/matches.go), per
// NFR2: no "since", no "load more", no client-driven paging control
// exists anywhere in this package. truncated (from ListPending) is
// surfaced in views.templ as a static note, never a paging control.
const defaultPendingLimit = 50

// Handlers holds the dependencies matches' route needs: the Store (for
// store.CanRead plus Matches()/Sync()/VideoScripts()/Verdicts()/
// Channels()/Roles()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// matchVideoView is the SyncedVideo half of one rendered pending match --
// the view-layer mirror of mcp/tools/matches.go's MatchVideoOutput.
// Metrics fields are nil when no video_metrics row exists yet (never a
// zero -- views.templ renders "no metrics synced yet" for that case).
type matchVideoView struct {
	Title       string
	PublishedAt *time.Time

	Views                      *int64
	AverageViewDurationSeconds *float64
	AverageViewPercentage      *float64
	Impressions                *int64
	ImpressionCTR              *float64
	MetricsMeasuredAt          *time.Time
}

// matchScriptView is a pending match's best-guess video_script -- the
// view-layer mirror of mcp/tools/matches.go's MatchScriptOutput.
// VerdictVersion/Verdict are the BOUND verdict version (LB3: the version
// the script was proposed under, never the Idea's current verdict).
// TargetPublishDate is nil for an undated script -- normal per FR36, not
// an error.
type matchScriptView struct {
	Title             string
	Status            store.VideoScriptStatus
	TargetPublishDate *time.Time
	VerdictVersion    int
	Verdict           store.VerdictValue
}

// pendingMatchView is one pending video_schedule_match assembled for
// List -- views.templ does no store calls itself, matching web/research's
// split (see this file's package doc comment). BestGuessScript is nil
// when the match's video_script_id is nil (a match recorded with no
// plausible candidate at all, see MatchStore.HasMatch's doc on issue
// #1652 -- normal, not an error); Confidence is 0 in that case.
type pendingMatchView struct {
	Video           matchVideoView
	BestGuessScript *matchScriptView
	Confidence      float64
}

// renderMatchVideo resolves syncedVideoID's SyncedVideo plus its latest
// VideoMetrics (if any), reproducing mcp/tools/matches.go's
// renderMatchVideo against the same store.SyncStore (LB5).
func renderMatchVideo(ctx context.Context, sync store.SyncStore, syncedVideoID uuid.UUID) (matchVideoView, error) {
	video, err := sync.GetByID(ctx, syncedVideoID)
	if err != nil {
		return matchVideoView{}, err
	}
	metrics, err := sync.LatestMetricsFor(ctx, syncedVideoID)
	if err != nil {
		return matchVideoView{}, err
	}

	out := matchVideoView{Title: video.Title, PublishedAt: video.PublishedAt}
	if metrics != nil {
		out.Views = metrics.Views
		out.AverageViewDurationSeconds = metrics.AverageViewDurationSeconds
		out.AverageViewPercentage = metrics.AverageViewPercentage
		out.Impressions = metrics.Impressions
		out.ImpressionCTR = metrics.ImpressionCTR
		measuredAt := metrics.MeasuredAt
		out.MetricsMeasuredAt = &measuredAt
	}
	return out, nil
}

// renderMatchScript resolves scriptID's VideoScript and its bound
// Verdict, reproducing mcp/tools/matches.go's renderMatchScript against
// the same store.VideoScriptStore/store.VerdictStore (LB5).
func renderMatchScript(ctx context.Context, videoScripts store.VideoScriptStore, verdicts store.VerdictStore, scriptID uuid.UUID) (matchScriptView, error) {
	script, err := videoScripts.GetByID(ctx, scriptID)
	if err != nil {
		return matchScriptView{}, err
	}
	verdict, err := verdicts.GetByID(ctx, script.VerdictID)
	if err != nil {
		return matchScriptView{}, err
	}
	return matchScriptView{
		Title:             script.Title,
		Status:            script.Status,
		TargetPublishDate: script.TargetPublishDate,
		VerdictVersion:    verdict.Version,
		Verdict:           verdict.Verdict,
	}, nil
}

// HandleList serves GET /channels/{id}/matches (FR7, FR8). The
// auth/404/403 preamble mirrors web/schedule.Handlers.HandleList and
// research.Handlers.HandleChannelIndex exactly (load-bearing, not
// stylistic, per this issue's body): resolve the signed-in Person (401),
// parse {id} (400), load the Channel (404 on pgx.ErrNoRows -- an unknown
// Channel 404s before authorization can turn it into a 403), then
// store.CanRead (403). Rows come from store.MatchStore.ListPending --
// the exact same store call listPendingMatchesHandler makes, with no
// parallel query (LB5).
func (h *Handlers) HandleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	person := auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	ch, err := h.store.Channels().GetByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	canRead, err := store.CanRead(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canRead {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	pending, truncated, err := h.store.Matches().ListPending(ctx, channelID, nil, defaultPendingLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sync := h.store.Sync()
	videoScripts := h.store.VideoScripts()
	verdicts := h.store.Verdicts()

	views := make([]pendingMatchView, 0, len(pending))
	for _, m := range pending {
		video, err := renderMatchVideo(ctx, sync, m.SyncedVideoID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		v := pendingMatchView{Video: video, Confidence: m.Confidence}
		if m.VideoScriptID != nil {
			script, err := renderMatchScript(ctx, videoScripts, verdicts, *m.VideoScriptID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			v.BestGuessScript = &script
		}
		views = append(views, v)
	}

	title := ch.Title + " pending matches"
	data := components.LayoutData{Title: title, User: person}
	if err := components.Render(w, r, title, List(data, ch, views, truncated)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
