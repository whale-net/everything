// Package youtube is the SOLE point of contact with Google's YouTube Data
// API v3 (`google.golang.org/api/youtube/v3`) and YouTube Analytics API v2
// (`google.golang.org/api/youtubeanalytics/v2`) anywhere in this repo --
// see ../ARCHITECTURE.md "MCP server" for the vendoring decision and
// //audience_score_system/deps for the compile-only smoke target proving
// both packages resolve under Bazel. Quota handling, error classification
// (errors.go), and revoked-credential detection (FR4) all live here once,
// so no other package should import google.golang.org/api/... directly --
// the only accepted exception is web/channel's own inline
// channels.list?mine=true resolver (#1571), which this package's
// ChannelInfo may absorb later (see this task's "Validation" section).
//
// Client wraps a single Channel's already-refreshing oauth2.TokenSource
// (audience_score_system/tokens.Store.TokenSource, #1571) -- this package
// never itself persists, refreshes, or logs a token; it only consumes one
// per call.
//
// Scaffold only (issue #1573): every Client method (here, schedule.go,
// metrics.go) is a stub returning errNotImplemented. Real ListSchedule/
// Metrics/ChannelInfo calls against the vendored youtube/v3 and
// youtubeanalytics/v2 services -- pagination, the authenticated-owner
// scheduled/private-draft path (FR14), a context deadline on every
// request, and errors.go's classify -- land in the Implementation phase.
// See ./fake for a fully working, in-memory Client double consumers
// (#1574/#1576/#1581) can depend on today without waiting for that.
package youtube

import (
	"context"
	"time"

	"golang.org/x/oauth2"
)

// Channel is the authorized Channel's own identity, resolved via
// ChannelInfo -- used by web/channel's connect callback (C2) to name the
// Channel it just connected.
type Channel struct {
	YouTubeChannelID string
	Title            string
}

// Client is the sanctioned interface every M1 consumer (worker's sync
// workflow #1574, mcp's schedule/metrics tools #1576/#1581) reads YouTube
// Data/Analytics through -- never the vendored youtube/v3 or
// youtubeanalytics/v2 services directly. fake.Client (./fake) satisfies
// this for consumer tests with no network call.
type Client interface {
	// ListSchedule returns every public upload AND Studio scheduled/private
	// draft for the authorized Channel (FR14), paginating internally so
	// callers never see a partial page. channelID is the Channel's YouTube
	// channel id (store.Channel.YouTubeChannelID), not this app's internal
	// uuid.
	ListSchedule(ctx context.Context, channelID string) ([]Video, error)

	// Metrics returns views, retention, and CTR/impressions for each of
	// videoIDs measured since since (FR21). A video with no Analytics rows
	// yet (e.g. just published) still yields a zero-valued VideoMetrics
	// entry for that id, never an error for that video alone.
	Metrics(ctx context.Context, channelID string, videoIDs []string, since time.Time) ([]VideoMetrics, error)

	// ChannelInfo resolves the authorized Channel's own id and title --
	// used by web/channel's connect callback (C2) to name a newly
	// connected Channel.
	ChannelInfo(ctx context.Context) (Channel, error)
}

// Option configures a Client returned by New -- e.g. a request timeout or
// an injected *http.Client so tests never make a live network call (this
// task's Testing section). Scaffold defines the type with the plumbing
// wired but no options yet, so New's signature is already stable for
// #1574/#1576/#1581 to depend on today.
type Option func(*clientConfig)

// clientConfig holds Option-configurable client settings. Empty in
// Scaffold; the Implementation phase adds fields as real options land.
type clientConfig struct{}

// client is Client's real implementation, wrapping a single Channel's
// oauth2.TokenSource.
//
// Scaffold only -- every method below (here, schedule.go, metrics.go) is a
// stub. Full implementation lands in the Implementation phase (this
// task's "Implementation" scope).
type client struct {
	ts  oauth2.TokenSource
	cfg *clientConfig
}

var _ Client = (*client)(nil)

// New returns a Client that calls the YouTube Data/Analytics APIs using ts
// for authorization -- ts is expected to be
// audience_score_system/tokens.Store.TokenSource(ctx, channelID)'s result,
// which already handles refresh and revocation bookkeeping on the token
// itself; this package only consumes tokens; it never persists or
// refreshes them, and never calls tokens.Store.MarkNeedsReauth directly --
// that stays the caller's job, driven off errors.go's ErrRevoked.
func New(ts oauth2.TokenSource, opts ...Option) Client {
	cfg := &clientConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &client{ts: ts, cfg: cfg}
}

func (c *client) ChannelInfo(ctx context.Context) (Channel, error) {
	return Channel{}, errNotImplemented
}

// errNotImplemented is every Scaffold-phase stub method's return value.
var errNotImplemented = notImplementedError{}

// notImplementedError is a trivial sentinel distinct from errors.New so
// every stub above returns the exact same comparable value (mirrors
// audience_score_system/tokens.errNotImplemented and web/auth's own).
type notImplementedError struct{}

func (notImplementedError) Error() string { return "not implemented" }
