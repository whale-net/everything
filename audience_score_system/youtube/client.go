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
// # HTTP transport
//
// New builds the vendored youtube/v3 and youtubeanalytics/v2 *Service
// values once, wiring them through option.WithHTTPClient rather than
// option.WithTokenSource: per google.golang.org/api/transport/http's
// NewClient, an explicit *http.Client always wins over a TokenSource, so
// this is what lets WithHTTPClient (below) point both services at an
// httptest server for tests with zero risk of a real network call ever
// slipping through -- see this task's Testing section. In production (no
// WithHTTPClient override), that *http.Client is oauth2.NewClient(ctx,
// ts): golang.org/x/oauth2's Transport calls ts.Token() on every request
// and returns that error verbatim (unwrapped) on failure, which is what
// lets classify (errors.go) find a wrapped *oauth2.RetrieveError via
// errors.As all the way from tokens.Store.
package youtube

import (
	"context"
	"errors"
	"net/http"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	youtubev3 "google.golang.org/api/youtube/v3"
	youtubeanalytics "google.golang.org/api/youtubeanalytics/v2"

	"github.com/whale-net/everything/libs/go/logging"
)

var logger = logging.Get("audience_score_system/youtube")

// defaultRequestTimeout bounds every individual YouTube Data/Analytics API
// call this package issues -- Client never blocks indefinitely (this
// task's Implementation scope).
const defaultRequestTimeout = 30 * time.Second

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

// Option configures a Client returned by New.
type Option func(*clientConfig)

// WithHTTPClient overrides the *http.Client the vendored youtube/v3 and
// youtubeanalytics/v2 service clients issue every request through, instead
// of the production default (an oauth2-authenticated client built from
// New's ts -- see the package doc comment's "HTTP transport" section).
// Tests use this to point both vendored services at an httptest server so
// no test in this package ever makes a live network call to Google (this
// task's Testing section). A nil hc is a no-op.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *clientConfig) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithRequestTimeout overrides defaultRequestTimeout, the deadline applied
// to every YouTube Data/Analytics API call this package issues.
func WithRequestTimeout(d time.Duration) Option {
	return func(c *clientConfig) {
		if d > 0 {
			c.requestTimeout = d
		}
	}
}

// withServiceOptions is an unexported, package-internal test seam (used by
// this package's own *_test.go files) for passing extra
// option.ClientOption values -- typically option.WithEndpoint(server.URL)
// -- to both vendored services' construction. Not part of the exported
// Option surface: WithHTTPClient already redirects every request an
// httptest server needs to see, and no real caller ever needs to override
// Google's API endpoint.
func withServiceOptions(opts ...option.ClientOption) Option {
	return func(c *clientConfig) {
		c.serviceOpts = append(c.serviceOpts, opts...)
	}
}

// clientConfig holds Option-configurable client settings.
type clientConfig struct {
	httpClient     *http.Client
	requestTimeout time.Duration
	serviceOpts    []option.ClientOption
}

// client is Client's real implementation, wrapping a single Channel's
// oauth2.TokenSource and the two vendored service clients built from it.
type client struct {
	ts  oauth2.TokenSource
	cfg *clientConfig

	yt  *youtubev3.Service
	ya  *youtubeanalytics.Service
	err error // set if either vendored service failed to construct
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
	cfg := &clientConfig{requestTimeout: defaultRequestTimeout}
	for _, opt := range opts {
		opt(cfg)
	}

	httpClient := cfg.httpClient
	if httpClient == nil {
		httpClient = oauth2.NewClient(context.Background(), ts)
	}
	svcOpts := append([]option.ClientOption{option.WithHTTPClient(httpClient)}, cfg.serviceOpts...)

	// Construction here only fails if the vendored client libraries
	// themselves reject the supplied options (e.g. an invalid endpoint) --
	// never from a network call, since an *http.Client is always supplied
	// above. New has no error return (it's part of Client's sanctioned,
	// already-stable signature), so a failure is deferred to the first
	// real method call and classified there like any other error.
	yt, ytErr := youtubev3.NewService(context.Background(), svcOpts...)
	ya, yaErr := youtubeanalytics.NewService(context.Background(), svcOpts...)

	return &client{
		ts:  ts,
		cfg: cfg,
		yt:  yt,
		ya:  ya,
		err: errors.Join(ytErr, yaErr),
	}
}

// withTimeout returns a child of ctx bounded by cfg.requestTimeout, and its
// cancel func -- every Client method call wraps its API call(s) in this so
// the client never blocks indefinitely.
func (c *client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.cfg.requestTimeout)
}

func (c *client) ChannelInfo(ctx context.Context) (Channel, error) {
	if c.err != nil {
		return Channel{}, classify(c.err)
	}

	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	resp, err := c.yt.Channels.List([]string{"snippet"}).Mine(true).Context(ctx).Do()
	if err != nil {
		return Channel{}, classify(err)
	}
	if len(resp.Items) == 0 {
		return Channel{}, ErrPermanent
	}

	item := resp.Items[0]
	ch := Channel{YouTubeChannelID: item.Id}
	if item.Snippet != nil {
		ch.Title = item.Snippet.Title
	}
	return ch, nil
}
