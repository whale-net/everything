package writeback

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/whale-net/everything/libs/go/argocd"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
)

// newTestArgoSyncActivities wires an *ArgoSyncActivities against an
// httptest-backed argocd.Client (mirroring libs/go/argocd/client_test.go's
// testClient helper) and a fresh fake.Registry seeded with one promotion,
// so TriggerArgoRefresh/PollArgoSyncStatus have a real promotion_id to
// write promotion_sync_event rows against -- see
// repository.PromotionRepository.RecordSyncEvent's FK-equivalent contract.
// PollInterval is set to 1ms so ExhaustsAttempts-style tests don't actually
// wait out the production 2-minute cadence.
func newTestArgoSyncActivities(t *testing.T, handler http.HandlerFunc) (*ArgoSyncActivities, *fake.Registry, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client, err := argocd.NewClient(argocd.Config{ServerURL: srv.URL, AuthToken: "test-token"}, srv.Client())
	require.NoError(t, err)

	registry := fake.New()
	promotion := registry.SeedPromotion(repository.Promotion{EnvironmentID: "env-1", TargetKey: "image:acme-widget"})

	return &ArgoSyncActivities{Client: client, Registry: registry, PollInterval: time.Millisecond}, registry, promotion.PromotionID
}

// runTriggerArgoRefresh/runPollArgoSyncStatus execute the activity method
// through a real Temporal activity environment rather than calling it
// directly with context.Background(): PollArgoSyncStatus calls
// activity.RecordHeartbeat(ctx, ...), which panics outside a real activity
// context -- testsuite.TestActivityEnvironment is the SDK's own way to unit
// test an activity method that does this, matching worker/release/
// plan_test.go's runResolvePlan helper (same rationale, activity.GetInfo
// there instead of RecordHeartbeat here).
func runTriggerArgoRefresh(t *testing.T, a *ArgoSyncActivities, in ArgoSyncInput) error {
	t.Helper()
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestActivityEnvironment()
	env.RegisterActivity(a.TriggerArgoRefresh)
	_, err := env.ExecuteActivity(a.TriggerArgoRefresh, in)
	return err
}

func runPollArgoSyncStatus(t *testing.T, a *ArgoSyncActivities, in ArgoSyncInput) (ArgoSyncResult, error) {
	t.Helper()
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestActivityEnvironment()
	env.RegisterActivity(a.PollArgoSyncStatus)
	val, err := env.ExecuteActivity(a.PollArgoSyncStatus, in)
	if err != nil {
		return ArgoSyncResult{}, err
	}
	var result ArgoSyncResult
	require.NoError(t, val.Get(&result))
	return result, nil
}

// TestArgoSyncActivities_TriggerArgoRefresh_CallsClientAndRecordsEvent
// proves FR1: TriggerArgoRefresh calls Client.Refresh with the
// project/name from ArgoSyncInput.Domain/ApplicationName, and records
// exactly one refresh_triggered promotion_sync_event row.
func TestArgoSyncActivities_TriggerArgoRefresh_CallsClientAndRecordsEvent(t *testing.T) {
	var gotProject, gotName string
	var calls int
	a, registry, promotionID := newTestArgoSyncActivities(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotProject = r.URL.Query().Get("project")
		gotName = r.URL.Path[len("/api/v1/applications/"):]
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})

	in := ArgoSyncInput{PromotionID: promotionID, Domain: "acme", ApplicationName: "foo-stage"}
	err := runTriggerArgoRefresh(t, a, in)
	require.NoError(t, err)

	require.Equal(t, 1, calls, "expected exactly one call to Client.Refresh")
	require.Equal(t, "acme", gotProject)
	require.Equal(t, "foo-stage", gotName)

	events, err := registry.Promotions().ListSyncEvents(t.Context(), promotionID)
	require.NoError(t, err)
	require.Len(t, events, 1, "expected exactly one promotion_sync_event row")
	require.Equal(t, repository.PromotionSyncEventSourceRefreshTriggered, events[0].Source)
	require.Equal(t, promotionID, events[0].PromotionID)
}

// TestArgoSyncActivities_TriggerArgoRefresh_ClientFailurePropagates proves
// a Client.Refresh failure surfaces as an error (this activity's own
// RetryPolicy, set by whichever workflow.ActivityOptions WritebackWorkflow
// wraps it in, is what covers retries -- see TriggerArgoRefresh's doc
// comment) and writes no promotion_sync_event row.
func TestArgoSyncActivities_TriggerArgoRefresh_ClientFailurePropagates(t *testing.T) {
	a, registry, promotionID := newTestArgoSyncActivities(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("argocd unavailable"))
	})

	err := runTriggerArgoRefresh(t, a, ArgoSyncInput{PromotionID: promotionID, Domain: "acme", ApplicationName: "foo-stage"})
	require.Error(t, err)

	events, err := registry.Promotions().ListSyncEvents(t.Context(), promotionID)
	require.NoError(t, err)
	require.Empty(t, events, "a failed Refresh call must not record a sync event")
}

// argoStatusServer returns an http.HandlerFunc serving the given
// (syncStatus, healthStatus, operationPhase) triples in order, one per
// GetStatus call -- letting a test script a specific attempt-by-attempt
// sequence.
func argoStatusServer(statuses [][3]string) http.HandlerFunc {
	call := 0
	return func(w http.ResponseWriter, r *http.Request) {
		i := call
		if i >= len(statuses) {
			i = len(statuses) - 1
		}
		call++
		triple := statuses[i]
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":{"sync":{"status":"` + triple[0] + `"},"health":{"status":"` + triple[1] + `"},"operationState":{"phase":"` + triple[2] + `"}}}`))
	}
}

// TestArgoSyncActivities_PollArgoSyncStatus_StopsEarlyOnSyncedHealthy
// proves FR4: when the first observation is already Synced/Healthy,
// PollArgoSyncStatus returns after attempt 1 -- only one GetStatus call,
// only one poll_observed row written.
func TestArgoSyncActivities_PollArgoSyncStatus_StopsEarlyOnSyncedHealthy(t *testing.T) {
	var calls int
	a, registry, promotionID := newTestArgoSyncActivities(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":{"sync":{"status":"Synced"},"health":{"status":"Healthy"},"operationState":{"phase":"Succeeded"}}}`))
	})

	result, err := runPollArgoSyncStatus(t, a, ArgoSyncInput{PromotionID: promotionID, Domain: "acme", ApplicationName: "foo-stage"})
	require.NoError(t, err)
	require.Equal(t, ArgoSyncResult{SyncStatus: "Synced", HealthStatus: "Healthy", OperationPhase: "Succeeded", Terminal: true}, result)
	require.Equal(t, 1, calls, "expected exactly one GetStatus call")

	events, err := registry.Promotions().ListSyncEvents(t.Context(), promotionID)
	require.NoError(t, err)
	require.Len(t, events, 1, "expected exactly one poll_observed row")
	require.Equal(t, repository.PromotionSyncEventSourcePollObserved, events[0].Source)
	require.Equal(t, "Synced", events[0].SyncStatus)
	require.Equal(t, "Healthy", events[0].HealthStatus)
	require.Equal(t, "Succeeded", events[0].OperationPhase)
}

// TestArgoSyncActivities_PollArgoSyncStatus_WaitsForHookToFinish is the
// direct regression test for the bug this whole change fixes: a migration
// Job implemented as a PostSync hook is excluded from ArgoCD's health
// rollup, so the Application can report Synced+Healthy on attempt 1 while
// the hook is still Running -- PollArgoSyncStatus must NOT stop early on
// that observation, and must only report Terminal once operationPhase
// flips to Succeeded on a later attempt.
func TestArgoSyncActivities_PollArgoSyncStatus_WaitsForHookToFinish(t *testing.T) {
	a, registry, promotionID := newTestArgoSyncActivities(t, argoStatusServer([][3]string{
		{"Synced", "Healthy", "Running"},
		{"Synced", "Healthy", "Running"},
		{"Synced", "Healthy", "Succeeded"},
	}))

	result, err := runPollArgoSyncStatus(t, a, ArgoSyncInput{PromotionID: promotionID, Domain: "acme", ApplicationName: "foo-stage"})
	require.NoError(t, err)
	require.Equal(t, ArgoSyncResult{SyncStatus: "Synced", HealthStatus: "Healthy", OperationPhase: "Succeeded", Terminal: true}, result)

	events, err := registry.Promotions().ListSyncEvents(t.Context(), promotionID)
	require.NoError(t, err)
	require.Len(t, events, 3, "expected one poll_observed row per attempt until the hook finished")
	require.False(t, isTerminalArgoSyncState(events[0].SyncStatus, events[0].HealthStatus, events[0].OperationPhase),
		"Synced+Healthy while the hook operation is Running must not be treated as terminal")
}

// TestArgoSyncActivities_PollArgoSyncStatus_HookFailureIsTerminal proves the
// other half of the fix: a Failed/Error operationPhase is terminal-failure
// even when health still reads Healthy (the non-hook resources the failed
// hook gates can look fine on their own).
func TestArgoSyncActivities_PollArgoSyncStatus_HookFailureIsTerminal(t *testing.T) {
	var calls int
	a, registry, promotionID := newTestArgoSyncActivities(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":{"sync":{"status":"Synced"},"health":{"status":"Healthy"},"operationState":{"phase":"Failed"}}}`))
	})

	result, err := runPollArgoSyncStatus(t, a, ArgoSyncInput{PromotionID: promotionID, Domain: "acme", ApplicationName: "foo-stage"})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, "Failed", result.OperationPhase)
	require.Equal(t, 1, calls, "expected exactly one GetStatus call")

	events, err := registry.Promotions().ListSyncEvents(t.Context(), promotionID)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

// TestArgoSyncActivities_PollArgoSyncStatus_StopsEarlyOnDegraded proves
// FR4's other stop-early condition: Degraded health, even with a non-Synced
// sync status, also stops after attempt 1.
func TestArgoSyncActivities_PollArgoSyncStatus_StopsEarlyOnDegraded(t *testing.T) {
	var calls int
	a, registry, promotionID := newTestArgoSyncActivities(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":{"sync":{"status":"OutOfSync"},"health":{"status":"Degraded"}}}`))
	})

	result, err := runPollArgoSyncStatus(t, a, ArgoSyncInput{PromotionID: promotionID, Domain: "acme", ApplicationName: "foo-stage"})
	require.NoError(t, err)
	require.Equal(t, ArgoSyncResult{SyncStatus: "OutOfSync", HealthStatus: "Degraded", Terminal: true}, result)
	require.Equal(t, 1, calls, "expected exactly one GetStatus call")

	events, err := registry.Promotions().ListSyncEvents(t.Context(), promotionID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "Degraded", events[0].HealthStatus)
}

// TestArgoSyncActivities_PollArgoSyncStatus_ExhaustsAttempts_NeverFails
// proves FR5/NFR3: when status never reaches a terminal state,
// PollArgoSyncStatus runs all 3 attempts, returns success (nil error) with
// the last-observed pair standing as "still pending", and writes 3
// poll_observed rows -- one per attempt.
func TestArgoSyncActivities_PollArgoSyncStatus_ExhaustsAttempts_NeverFails(t *testing.T) {
	var calls int
	a, registry, promotionID := newTestArgoSyncActivities(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":{"sync":{"status":"OutOfSync"},"health":{"status":"Progressing"}}}`))
	})

	result, err := runPollArgoSyncStatus(t, a, ArgoSyncInput{PromotionID: promotionID, Domain: "acme", ApplicationName: "foo-stage"})
	require.NoError(t, err, "PollArgoSyncStatus must never return an error for 'still pending' (FR5)")
	require.Equal(t, ArgoSyncResult{SyncStatus: "OutOfSync", HealthStatus: "Progressing", Terminal: false}, result)
	require.Equal(t, pollArgoSyncMaxAttempts, calls, "expected exactly pollArgoSyncMaxAttempts GetStatus calls, no more")

	events, err := registry.Promotions().ListSyncEvents(t.Context(), promotionID)
	require.NoError(t, err)
	require.Len(t, events, pollArgoSyncMaxAttempts, "expected one poll_observed row per attempt")
	for _, e := range events {
		require.Equal(t, repository.PromotionSyncEventSourcePollObserved, e.Source)
	}
}

// TestArgoSyncActivities_PollArgoSyncStatus_TransitionsToTerminalMidway
// proves the loop bound is per-observation, not fixed: an OutOfSync/
// Progressing pair on attempt 1 that becomes Synced/Healthy on attempt 2
// stops at attempt 2, not attempt 3 -- exercising argoStatusServer's
// sequenced-response helper.
func TestArgoSyncActivities_PollArgoSyncStatus_TransitionsToTerminalMidway(t *testing.T) {
	a, registry, promotionID := newTestArgoSyncActivities(t, argoStatusServer([][3]string{
		{"OutOfSync", "Progressing", ""},
		{"Synced", "Healthy", "Succeeded"},
		{"Synced", "Healthy", "Succeeded"},
	}))

	result, err := runPollArgoSyncStatus(t, a, ArgoSyncInput{PromotionID: promotionID, Domain: "acme", ApplicationName: "foo-stage"})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, "Synced", result.SyncStatus)

	events, err := registry.Promotions().ListSyncEvents(t.Context(), promotionID)
	require.NoError(t, err)
	require.Len(t, events, 2, "expected exactly 2 poll_observed rows: attempt 1 (pending) + attempt 2 (terminal)")
}

// TestArgoSyncActivities_PollArgoSyncStatus_NoClientConfigured proves the
// "not configured" fail-fast path (mirrors writeback.Recorder's own
// Registry-not-configured check) -- a wiring bug, not an expected runtime
// state. Called directly (no activity environment needed): the nil-Client
// check returns before ever reaching activity.RecordHeartbeat.
func TestArgoSyncActivities_PollArgoSyncStatus_NoClientConfigured(t *testing.T) {
	a := &ArgoSyncActivities{}
	_, err := a.PollArgoSyncStatus(t.Context(), ArgoSyncInput{PromotionID: "promo-1"})
	require.Error(t, err)
}

// TestNoopArgoSyncActivities_NoOps proves NoopArgoSyncActivities (the
// ARGOCD_SERVER-unset fallback) skips the ArgoCD call entirely and reports
// success without needing a Client or Registry at all -- a zero-value
// NoopArgoSyncActivities is fully usable. Called directly: neither method
// touches anything activity-context-dependent.
func TestNoopArgoSyncActivities_NoOps(t *testing.T) {
	var n NoopArgoSyncActivities

	err := n.TriggerArgoRefresh(t.Context(), ArgoSyncInput{PromotionID: "promo-1", Domain: "acme", ApplicationName: "foo-stage"})
	require.NoError(t, err)

	result, err := n.PollArgoSyncStatus(t.Context(), ArgoSyncInput{PromotionID: "promo-1", Domain: "acme", ApplicationName: "foo-stage"})
	require.NoError(t, err)
	require.Equal(t, ArgoSyncResult{}, result, "Noop must report a zero-value result, never a synthetic terminal observation")
}

// TestSelectArgoSyncActivities_EmptyServerReturnsNoop proves the
// ARGOCD_SERVER-unset branch of the opt-in gate ../main.go applies: an
// empty server string returns a NoopArgoSyncActivities, not a real client.
func TestSelectArgoSyncActivities_EmptyServerReturnsNoop(t *testing.T) {
	got, err := SelectArgoSyncActivities("", "", fake.New())
	require.NoError(t, err)
	require.IsType(t, NoopArgoSyncActivities{}, got)
}

// TestSelectArgoSyncActivities_ServerSetReturnsRealActivities proves the
// ARGOCD_SERVER-set branch: a non-empty server returns a real
// *ArgoSyncActivities wired up with a working argocd.Client and the passed
// registry, not the no-op fallback.
func TestSelectArgoSyncActivities_ServerSetReturnsRealActivities(t *testing.T) {
	registry := fake.New()
	got, err := SelectArgoSyncActivities("https://argocd.example.com", "test-token", registry)
	require.NoError(t, err)

	real, ok := got.(*ArgoSyncActivities)
	require.True(t, ok, "expected a *ArgoSyncActivities, got %T", got)
	require.NotNil(t, real.Client)
	require.Equal(t, repository.Registry(registry), real.Registry)
}

// TestSelectArgoSyncActivities_InvalidConfigReturnsError proves a
// misconfigured server URL (argocd.NewClient's own validation) surfaces as
// an error rather than a silently broken client -- ARGOCD_AUTH_TOKEN unset
// while ARGOCD_SERVER is set is exactly the "server present but token
// missing" wiring mistake this guards against.
func TestSelectArgoSyncActivities_InvalidConfigReturnsError(t *testing.T) {
	_, err := SelectArgoSyncActivities("https://argocd.example.com", "", fake.New())
	require.Error(t, err)
}

// FakePublisher for testing records all publishes.
type FakePublisher struct {
	events []PublishedEvent
}

type PublishedEvent struct {
	PromotionID string
	EventKind   string
	EventStatus string
}

func NewFakePublisher() *FakePublisher {
	return &FakePublisher{events: []PublishedEvent{}}
}

func (f *FakePublisher) Publish(promotionID, eventKind, eventStatus string) {
	f.events = append(f.events, PublishedEvent{
		PromotionID: promotionID,
		EventKind:   eventKind,
		EventStatus: eventStatus,
	})
}

// TestArgoSyncActivities_RecordSyncEvent_PublishesAfterWrite verifies that
// recordSyncEvent publishes an event after the database write commits (FR7a/FR7b).
func TestArgoSyncActivities_RecordSyncEvent_PublishesAfterWrite(t *testing.T) {
	a, _, promotionID := newTestArgoSyncActivities(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})
	pub := NewFakePublisher()
	a.Publisher = pub

	_, err := a.recordSyncEvent(t.Context(), promotionID, repository.PromotionSyncEventSourceRefreshTriggered, "", "", "")
	require.NoError(t, err)

	// Verify event was published with correct payload
	require.Len(t, pub.events, 1, "expected exactly one published event")
	event := pub.events[0]
	require.Equal(t, promotionID, event.PromotionID)
	require.Equal(t, repository.PromotionSyncEventSourceRefreshTriggered, event.EventKind)
	require.Equal(t, "pending", event.EventStatus)
}

// TestArgoSyncActivities_RecordSyncEvent_NoPublisherConfigured verifies that
// when publisher is nil, no publish is attempted (graceful degradation).
func TestArgoSyncActivities_RecordSyncEvent_NoPublisherConfigured(t *testing.T) {
	a, reg, promotionID := newTestArgoSyncActivities(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})
	a.Publisher = nil // Explicitly nil

	_, err := a.recordSyncEvent(t.Context(), promotionID, repository.PromotionSyncEventSourcePollObserved, "Synced", "Healthy", "Succeeded")
	require.NoError(t, err)

	// Verify the event was still recorded in the database
	events, err := reg.Promotions().ListSyncEvents(t.Context(), promotionID)
	require.NoError(t, err)
	require.Len(t, events, 1)
}

// TestArgoSyncActivities_RecordSyncEvent_PublishesCorrectEventKind verifies that
// different sync event sources publish with the correct event kind.
func TestArgoSyncActivities_RecordSyncEvent_PublishesCorrectEventKind(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		wantKind string
	}{
		{"RefreshTriggered", repository.PromotionSyncEventSourceRefreshTriggered, repository.PromotionSyncEventSourceRefreshTriggered},
		{"PollObserved", repository.PromotionSyncEventSourcePollObserved, repository.PromotionSyncEventSourcePollObserved},
		{"RetryTriggered", repository.PromotionSyncEventSourceRetryTriggered, repository.PromotionSyncEventSourceRetryTriggered},
		{"RetryObserved", repository.PromotionSyncEventSourceRetryObserved, repository.PromotionSyncEventSourceRetryObserved},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, promotionID := newTestArgoSyncActivities(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{}`))
			})
			pub := NewFakePublisher()
			a.Publisher = pub

			_, err := a.recordSyncEvent(t.Context(), promotionID, tt.source, "Synced", "Healthy", "Succeeded")
			require.NoError(t, err)

			require.Len(t, pub.events, 1)
			require.Equal(t, tt.wantKind, pub.events[0].EventKind)
		})
	}
}
