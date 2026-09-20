// Command verify_multi_replica_sse is the committed, re-runnable procedure
// for #1706 (closes #1138's NFR17(a) exit criterion): with app-registry's
// UI running at >=2 replicas against a real RabbitMQ, does an event
// published for one release run / promotion reach SSE subscribers attached
// to every replica, not one at random?
//
// This is a broker property (server-named, non-durable, auto-delete queue
// per replica -- libs/go/htmxsse's Hub) and cannot be asserted broker-free;
// see tools/app_registry/TESTING.md's "Live environment testing (NFR17)"
// section for the full procedure this tool is one step of, including how
// to get two locally-forwarded ports that each land on a *different* UI
// pod. This tool never goes through the Service -- one Service in front of
// two pods gives no control over which pod a subscriber lands on, so the
// two base URLs given via flags must each be a `kubectl port-forward
// pod/<name> <port>:8000` to a *different* pod, not the Service.
//
// What this tool does NOT do: trigger a real release or a real promotion.
// A real release run is not required to exercise the fan-out (#1706's own
// mechanics note) -- it inserts the minimum real `release_run`/
// `release_run_target` and `app`/`build`/`artifact`/`promotion` rows
// directly so GetRelease/GetPromotionDetails succeed (a nonexistent id
// would make the SSE fragment render fail, and htmxsse.Handler's contract
// is to write nothing for a fragment error -- see libs/go/htmxsse/
// handler.go -- which would make a broken fan-out and a missing test
// fixture look identical). It then hand-publishes one event per topic
// family directly to the app-registry.htmxsse exchange, exactly as an
// operator running `rabbitmqadmin publish` by hand would, and asserts only
// that both subscribers received *a* push -- never an exact count
// (duplicate/bursty publishes are normal and harmless; the fragment
// re-reads current state at delivery).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/tools/app_registry/events"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("FAIL: %v", err)
	}
}

func run() error {
	var (
		pgURL     = flag.String("pg-url", "postgres://postgres:password@localhost:5432/app_registry?sslmode=disable", "Postgres connection string (Tilt default: forwarded localhost:5432)")
		rabbitURL = flag.String("rabbitmq-url", "amqp://rabbit:password@localhost:5672/app-registry-dev", "RabbitMQ connection string (Tilt default: forwarded localhost:5672, app-registry-dev vhost)")
		pod1Base  = flag.String("pod1-addr", "http://localhost:8000", "Base URL of the per-pod port-forward for replica 1 (kubectl port-forward pod/<pod1> <port>:8000)")
		pod2Base  = flag.String("pod2-addr", "http://localhost:8001", "Base URL of the per-pod port-forward for replica 2 (kubectl port-forward pod/<pod2> <port>:8000)")
		readyWait = flag.Duration("ready-timeout", 15*time.Second, "How long to wait for all 4 SSE subscribers to connect and receive their initial full-state frame before publishing")
		pushWait  = flag.Duration("push-timeout", 15*time.Second, "How long to wait, after publishing, for each subscriber to receive its push")
	)
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.NewPool(ctx, *pgURL)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()

	releaseRunID, promotionID, err := seedFixtures(ctx, pool)
	if err != nil {
		return fmt.Errorf("seed fixture data: %w", err)
	}
	log.Printf("seeded release_run_id=%s promotion_id=%s", releaseRunID, promotionID)

	conn, err := rmq.NewConnectionFromURL(*rabbitURL)
	if err != nil {
		return fmt.Errorf("connect to rabbitmq: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	pub, err := rmq.NewPublisherWithExchange(conn, events.ExchangeName)
	if err != nil {
		return fmt.Errorf("declare exchange %q: %w", events.ExchangeName, err)
	}
	defer pub.Close() //nolint:errcheck

	subs := []*subscriber{
		newSubscriber("release-run/pod1", *pod1Base+"/releases/"+releaseRunID+"/status/sse", events.TopicForReleaseRun(releaseRunID)),
		newSubscriber("release-run/pod2", *pod2Base+"/releases/"+releaseRunID+"/status/sse", events.TopicForReleaseRun(releaseRunID)),
		newSubscriber("promotion/pod1", *pod1Base+"/promotions/"+promotionID+"/status/sse", events.TopicForPromotion(promotionID)),
		newSubscriber("promotion/pod2", *pod2Base+"/promotions/"+promotionID+"/status/sse", events.TopicForPromotion(promotionID)),
	}

	var wg sync.WaitGroup
	for _, s := range subs {
		wg.Add(1)
		go func(s *subscriber) {
			defer wg.Done()
			s.run(ctx)
		}(s)
	}

	log.Printf("waiting up to %s for all %d subscribers to connect and receive their initial full-state frame...", *readyWait, len(subs))
	if err := waitReady(subs, *readyWait); err != nil {
		cancel()
		wg.Wait()
		return fmt.Errorf("subscribers never became ready (mechanics violated -- check per-pod port-forwards and AUTH_MODE=none): %w", err)
	}
	log.Printf("all subscribers connected. publishing one hand-crafted event per topic family to %s (re-publishing every 2s until every subscriber has pushed, up to %s -- see publishUntilPushed's doc comment for why a single publish is not enough)...", events.ExchangeName, *pushWait)

	publish := func() error {
		if err := publishOne(ctx, pub, events.TopicForReleaseRun(releaseRunID), events.ReleaseRunEventPayload{
			ReleaseRunID: releaseRunID, EventKind: "nfr17a_manual_verify", EventStatus: "n/a",
		}); err != nil {
			return fmt.Errorf("publish release_run event: %w", err)
		}
		if err := publishOne(ctx, pub, events.TopicForPromotion(promotionID), events.EventPayload{
			PromotionID: promotionID, EventKind: "nfr17a_manual_verify", EventStatus: "n/a",
		}); err != nil {
			return fmt.Errorf("publish promotion event: %w", err)
		}
		return nil
	}
	if err := publishUntilPushed(ctx, subs, publish, *pushWait); err != nil {
		cancel()
		wg.Wait()
		return err
	}
	cancel()
	wg.Wait()

	fmt.Println()
	fmt.Println("=== NFR17(a) multi-replica SSE fan-out result ===")
	allOK := true
	for _, s := range subs {
		result := "RECEIVED"
		if s.pushResult != nil {
			result = "MISSING (" + s.pushResult.Error() + ")"
			allOK = false
		}
		fmt.Printf("  %-16s topic=%-40s %s\n", s.name, s.topic, result)
	}
	fmt.Println()

	if !allOK {
		fmt.Println("FAIL: at least one subscriber did not receive the push -- this is the exact")
		fmt.Println("failure signature a shared/durable queue (or a Service-routed connection)")
		fmt.Println("would produce: one replica's subscriber sees the event, the other does not.")
		return errors.New("multi-replica SSE fan-out verification failed")
	}
	fmt.Println("PASS: both replicas' subscribers received a push for their own hand-published event.")
	return nil
}

func publishOne(ctx context.Context, pub *rmq.Publisher, routingKey string, payload any) error {
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return pub.Publish(pctx, events.ExchangeName, routingKey, payload)
}

// waitReady blocks until every subscriber has connected and consumed its
// initial full-state frame, or timeout elapses for any one of them.
func waitReady(subs []*subscriber, timeout time.Duration) error {
	var wg sync.WaitGroup
	errs := make([]error, len(subs))
	for i, s := range subs {
		wg.Add(1)
		go func(i int, s *subscriber) {
			defer wg.Done()
			select {
			case err := <-s.ready:
				errs[i] = err
			case <-time.After(timeout):
				errs[i] = errors.New("timed out waiting for initial frame")
			}
		}(i, s)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("%s: %w", subs[i].name, err)
		}
	}
	return nil
}

// publishUntilPushed calls publish immediately, then again every 2s until
// either every subscriber has received its push or timeout elapses,
// recording each subscriber's outcome on its own pushResult (never aborts
// early, so every subscriber's own pass/fail is always reported instead of
// only the first failure).
//
// A single publish is not enough: htmxsse.Hub's broker attach is lazy
// (triggered by the pod's first-ever Subscribe call, see hub.go's
// attachOnce) and asynchronous -- connect, channel-open, exchange-declare,
// queue-declare, and bind all happen on a background goroutine, so
// "subscriber received its initial full-state frame" (this tool's ready
// signal) can fire before that pod's Hub has finished binding to the
// exchange. Against a pod that has just started (every run of this tool,
// since it targets freshly port-forwarded pods) that race is far more
// likely to lose a single publish than it would be in a long-running
// pod. Re-publishing is safe: FR's own mechanics note (this tool's package
// doc) that duplicate/bursty publishes are normal and harmless, and this
// function asserts only that each subscriber eventually received *a* push,
// never a count.
func publishUntilPushed(ctx context.Context, subs []*subscriber, publish func() error, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	var wg sync.WaitGroup
	for _, s := range subs {
		wg.Add(1)
		go func(s *subscriber) {
			defer wg.Done()
			select {
			case err := <-s.pushed:
				s.pushResult = err
			case <-time.After(time.Until(deadline)):
				s.pushResult = errors.New("timed out waiting for post-publish frame")
			}
		}(s)
	}

	allDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(allDone)
	}()

	if err := publish(); err != nil {
		return err
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-allDone:
			return nil
		case <-time.After(time.Until(deadline)):
			<-allDone // outcomes are already being recorded by the per-subscriber goroutines above
			return nil
		case <-ctx.Done():
			<-allDone
			return nil
		case <-ticker.C:
			if err := publish(); err != nil {
				log.Printf("re-publish failed (will retry): %v", err)
			}
		}
	}
}

// subscriber is one SSE connection to one pod for one topic. ready fires
// once the connection is open and the initial full-state frame (always
// sent on connect, regardless of any publish -- see libs/go/htmxsse's
// Handler) has been consumed, so the caller knows it is safe to publish:
// htmxsse.Handler registers the Hub subscription before producing that
// first frame, so "first frame received" implies "subscription active".
// pushed fires on the *next* frame after that -- the one a hand-published
// event is expected to trigger.
type subscriber struct {
	name       string
	url        string
	topic      string
	ready      chan error
	pushed     chan error
	pushResult error
}

func newSubscriber(name, url, topic string) *subscriber {
	return &subscriber{name: name, url: url, topic: topic, ready: make(chan error, 1), pushed: make(chan error, 1)}
}

func (s *subscriber) run(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		s.ready <- err
		s.pushed <- err
		return
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.ready <- err
		s.pushed <- err
		return
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("unexpected status %d (expected 200 -- AUTH_MODE=none required)", resp.StatusCode)
		s.ready <- err
		s.pushed <- err
		return
	}

	wantEvent := "event: " + s.topic
	frames := 0
	readySent := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		if scanner.Text() != wantEvent {
			continue
		}
		frames++
		if frames == 1 {
			readySent = true
			s.ready <- nil
			continue
		}
		// Second (or later) non-keepalive frame for this topic: the push.
		s.pushed <- nil
		return
	}

	// Stream ended (context cancelled, or the server closed it) before a
	// push arrived.
	err = scanner.Err()
	if err == nil {
		err = errors.New("stream closed before a post-publish frame arrived")
	}
	if !readySent {
		s.ready <- err
	}
	s.pushed <- err
}

// seedFixtures inserts the minimum real rows GetRelease and
// GetPromotionDetails need to succeed (a nonexistent id would make the SSE
// fragment render fail -- see this file's package doc). Every row is
// tagged nfr17a-verify and suffixed with the current time so repeated runs
// never collide; nothing here is cleaned up afterward, matching
// scripts/seed_tilt_walkthrough.py's "purely additive" local-dev
// convention.
func seedFixtures(ctx context.Context, pool *pgxpool.Pool) (releaseRunID, promotionID string, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	owner := "nfr17a-verify-" + suffix

	// release_run + release_run_target -- no FK to `app`, so this alone is
	// enough for GetRelease to succeed (server/handlers/release.go).
	if err := tx.QueryRow(ctx, `
		INSERT INTO release_run (triggered_by, requested_scope, resolved_plan, temporal_workflow_id)
		VALUES ('nfr17a-verify', 'nfr17a-verify: multi-replica SSE fan-out check', '{}'::jsonb, $1)
		RETURNING release_run_id`, "nfr17a-verify-workflow-"+suffix).Scan(&releaseRunID); err != nil {
		return "", "", fmt.Errorf("insert release_run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO release_run_target (release_run_id, owner_full_name, kind, state)
		VALUES ($1, $2, 'image', 'queued')`, releaseRunID, owner); err != nil {
		return "", "", fmt.Errorf("insert release_run_target: %w", err)
	}

	// app -> build -> artifact -> promotion, so GetPromotionDetails
	// (server/repository/postgres/promotion.go's promotionSelectBase joins
	// promotion -> environment -> artifact only, never app_manifest) can
	// join through to a real artifact and a real (pre-seeded, migration
	// 002) environment. `app` itself carries no deploy_unit column since
	// migration 008 moved that concept onto app_manifest's generated
	// column; artifact.kind='image' is what actually selects the image
	// path, independent of any manifest.
	var appID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO app (domain, name, status)
		VALUES ('nfr17a-verify', $1, 'active')
		RETURNING app_id`, "verify-"+suffix).Scan(&appID); err != nil {
		return "", "", fmt.Errorf("insert app: %w", err)
	}
	var buildID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO build (git_sha, git_ref, workflow_run_id, actor)
		VALUES ($1, 'refs/heads/main', $2, 'nfr17a-verify')
		RETURNING build_id`, "nfr17a-verify-"+suffix, "nfr17a-verify-run-"+suffix).Scan(&buildID); err != nil {
		return "", "", fmt.Errorf("insert build: %w", err)
	}
	// state='published' (not the column's absent default) is required by
	// artifact_state_shape: published is the only state that allows a
	// non-null digest and build_id together, both of which this fixture
	// needs to be a real (not allocated/publishing-placeholder) artifact.
	// version_source has no default either ('registry' matches how a real
	// CI-driven publish records it, vs. 'tag' for an adopted tag-only row).
	var artifactID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO artifact (kind, app_id, repository, version, digest, build_id, state, version_source)
		VALUES ('image', $1, 'ghcr.io/whale-net/nfr17a-verify', 'v0.0.1', $2, $3, 'published', 'registry')
		RETURNING artifact_id`, appID, "sha256:nfr17a-verify-"+suffix, buildID).Scan(&artifactID); err != nil {
		return "", "", fmt.Errorf("insert artifact: %w", err)
	}
	var envID string
	if err := tx.QueryRow(ctx, `SELECT environment_id FROM environment WHERE key = 'dev'`).Scan(&envID); err != nil {
		return "", "", fmt.Errorf("look up 'dev' environment (expected pre-seeded by migration 002): %w", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO promotion (environment_id, target_key, artifact_id, state)
		VALUES ($1, $2, $3, 'active')
		RETURNING promotion_id`, envID, "image:"+owner, artifactID).Scan(&promotionID); err != nil {
		return "", "", fmt.Errorf("insert promotion: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return releaseRunID, promotionID, nil
}
