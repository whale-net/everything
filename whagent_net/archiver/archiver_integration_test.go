//go:build integration

// Issue #2244's Testing section: proves Archiver.RunOnce's selection,
// write-order, crash-safety, and idempotency contracts against a real
// Postgres (via //libs/go/dbtest, migrated with whagent-net's own embedded
// schema) and a real MinIO (via testcontainers-go) -- same
// real-dependencies-over-mocks precedent as
// //whagent_net/session:session_integration_test's
// transcript_archive_integration_test.go, which this file's MinIO/DB
// helpers deliberately mirror (down to using the exact same package-local
// helper names) so both suites read the same way.
//
// This file lives in package main (not an external "main_test" package)
// specifically so it can reach archiver.go's unexported helpers
// (pageWholeHotTranscript, encodeArchiveObject, archiveObjectKey,
// Archiver.s3/store fields) directly -- the crash-safety tests below need
// to reproduce the *exact* partial state a real crash would leave (e.g.
// "object uploaded, index row not yet committed") without a special test
// hook in the production code, and the only way to do that is to run the
// same private building blocks archiveSession itself runs, stopping short
// of the step that would represent "the crash".
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //whagent_net/archiver:archiver_integration_test --test_output=all
package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
	"github.com/whale-net/everything/whagent_net/session"
)

const (
	archiverTestMinioUser = "minioadmin"
	archiverTestMinioPass = "minioadmin"
	archiverTestBucket    = "whagent-archiver-test"
)

// startArchiverMinIO starts a throwaway MinIO container and returns a
// ready *s3.Client whose bucket already exists -- same precedent as
// libs/go/s3/s3_integration_test.go's startMinIO and session package's
// startArchiveMinIO.
func startArchiverMinIO(ctx context.Context, t *testing.T) *s3.Client {
	t.Helper()

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "minio/minio:latest",
			ExposedPorts: []string{"9000/tcp"},
			Env: map[string]string{
				"MINIO_ROOT_USER":     archiverTestMinioUser,
				"MINIO_ROOT_PASSWORD": archiverTestMinioPass,
			},
			Cmd:        []string{"server", "/data"},
			WaitingFor: wait.ForHTTP("/minio/health/live").WithPort("9000/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err, "start minio container")
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })

	host, err := ctr.Host(ctx)
	require.NoError(t, err, "minio host")
	port, err := ctr.MappedPort(ctx, "9000/tcp")
	require.NoError(t, err, "minio port")
	endpoint := fmt.Sprintf("http://%s:%s", host, port.Port())

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: archiverTestMinioUser, SecretAccessKey: archiverTestMinioPass}, nil
		})),
	)
	require.NoError(t, err, "load aws config")
	raw := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	_, err = raw.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(archiverTestBucket)})
	require.NoError(t, err, "create test bucket")

	client, err := s3.NewClient(ctx, s3.Config{
		Bucket:    archiverTestBucket,
		Region:    "us-east-1",
		Endpoint:  endpoint,
		AccessKey: archiverTestMinioUser,
		SecretKey: archiverTestMinioPass,
	})
	require.NoError(t, err, "s3.NewClient")
	return client
}

// newArchiverDB provisions an isolated Postgres database via dbtest and
// applies every migration in whagent-net's own embedded schema
// (schema.Migrations) -- same pattern as session package's newDB.
func newArchiverDB(t *testing.T) *dbtest.Postgres {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return db
}

// newTestArchiver builds a real *Archiver against pool/client, with a
// quiet logger (logging.Get, no Configure call -- same as production
// wiring in main.go minus the OTLP/JSON setup, which this test has no
// interest in observing).
func newTestArchiver(pool *pgxpool.Pool, client *s3.Client, cfg Config) *Archiver {
	return NewArchiver(pool, client, cfg, logging.Get("archiver_test"))
}

// defaultArchiverTestConfig is a Config with a short-but-nonzero TTL, so
// tests control eligibility purely via each fixture's forced updated_at
// rather than needing to wait out a real-world-sized TTL.
func defaultArchiverTestConfig(bucket string) Config {
	return Config{
		S3Bucket:      bucket,
		TranscriptTTL: time.Hour,
		ScanInterval:  time.Minute,
		BatchSize:     50,
	}
}

// createArchiverFixtureSession creates a session with nEvents transcript
// events, sets its status, and force-writes updated_at directly via raw
// SQL (session.Store has no API for backdating a row -- UpdateStatus
// always stamps NOW()) so tests can place a session anywhere relative to
// Config.TranscriptTTL without an artificial sleep.
func createArchiverFixtureSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, status session.Status, updatedAt time.Time, nEvents int) (uuid.UUID, []events.Event) {
	t.Helper()

	store := session.New(pool, nil)
	subject := session.Subject{
		Iss:  "https://issuer.example.com",
		Sub:  "user-" + uuid.NewString(),
		Kind: session.SubjectKindHuman,
	}
	sess := &session.Session{
		SessionID:  uuid.New(),
		Subject:    subject,
		OnBehalfOf: subject,
		AgentID:    "test-agent",
		Model:      "test-model",
		Status:     session.StatusRunning,
	}
	require.NoError(t, store.Sessions().Create(ctx, sess))

	evs := make([]events.Event, 0, nEvents)
	for i := 0; i < nEvents; i++ {
		ev, err := store.Transcript().Append(ctx, sess.SessionID, 0, "test.archiver_event", []byte(fmt.Sprintf(`{"i":%d}`, i)))
		require.NoError(t, err)
		evs = append(evs, ev)
	}

	if status != session.StatusRunning {
		require.NoError(t, store.Sessions().UpdateStatus(ctx, sess.SessionID, status, nil))
	}

	_, err := pool.Exec(ctx, `UPDATE sessions SET updated_at = $1 WHERE session_id = $2`, updatedAt, sess.SessionID)
	require.NoError(t, err, "force updated_at for TTL-eligibility fixture")

	return sess.SessionID, evs
}

// hotRowCount is a raw-SQL fact check on transcript_event, independent of
// TranscriptStore.Read (which would itself consult the archive tier) --
// exactly what "hot rows gone"/"hot rows still intact" needs to mean in
// this file.
func hotRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM transcript_event WHERE session_id = $1`, sessionID).Scan(&n))
	return n
}

// assertArchivedEventsEqual decodes the object at key and compares it
// field-by-field against want -- CommittedAt is compared via time.Equal,
// not require.Equal, for the same reason session package's
// assertEventsEqual documents (JSON round-tripping can change a
// time.Time's Location/monotonic reading without changing the instant).
func assertArchivedEventsEqual(t *testing.T, ctx context.Context, client *s3.Client, key string, want []events.Event) {
	t.Helper()
	data, err := client.Download(ctx, key)
	require.NoError(t, err, "download archived object")
	got, err := session.DecodeArchiveObject(data)
	require.NoError(t, err, "decode archived object")

	require.Len(t, got, len(want))
	for i := range want {
		require.Equal(t, want[i].EventID, got[i].EventID, "event[%d].EventID", i)
		require.Equal(t, want[i].SessionID, got[i].SessionID, "event[%d].SessionID", i)
		require.Equal(t, want[i].Seq, got[i].Seq, "event[%d].Seq", i)
		require.Equal(t, want[i].Turn, got[i].Turn, "event[%d].Turn", i)
		require.Equal(t, want[i].Type, got[i].Type, "event[%d].Type", i)
		require.JSONEq(t, string(want[i].Payload), string(got[i].Payload), "event[%d].Payload", i)
		require.True(t, want[i].CommittedAt.Equal(got[i].CommittedAt), "event[%d].CommittedAt", i)
	}
}

// TestArchiver_RunOnce_ArchivesTerminalSessionPastTTL is the issue's
// headline Testing case: a terminal session past TTL is archived -- object
// present with byte-identical (round-tripped) content, index row correct
// (event_count/min_seq/max_seq), hot rows gone, hot_trimmed_at set.
func TestArchiver_RunOnce_ArchivesTerminalSessionPastTTL(t *testing.T) {
	ctx := context.Background()
	client := startArchiverMinIO(ctx, t)
	db := newArchiverDB(t)
	cfg := defaultArchiverTestConfig(client.GetBucket())
	archiver := newTestArchiver(db.Pool, client, cfg)

	sessionID, want := createArchiverFixtureSession(t, ctx, db.Pool, session.StatusDone, time.Now().Add(-2*cfg.TranscriptTTL), 5)

	archived, err := archiver.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, archived)

	key := archiveObjectKey(sessionID)
	exists, err := client.Exists(ctx, key)
	require.NoError(t, err)
	require.True(t, exists, "archive object must be uploaded")
	assertArchivedEventsEqual(t, ctx, client, key, want)

	idx, err := archiver.store.Archive().Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, idx, "archive index row must be committed")
	require.Equal(t, int64(len(want)), idx.EventCount)
	require.Equal(t, want[0].Seq, idx.MinSeq)
	require.Equal(t, want[len(want)-1].Seq, idx.MaxSeq)
	require.NotNil(t, idx.HotTrimmedAt, "hot_trimmed_at must be set")

	require.Equal(t, 0, hotRowCount(t, ctx, db.Pool, sessionID), "hot rows must be trimmed")
}

// TestArchiver_RunOnce_TerminalSessionInsideTTL_Untouched proves a
// terminal session still inside the TTL window is left completely alone.
func TestArchiver_RunOnce_TerminalSessionInsideTTL_Untouched(t *testing.T) {
	ctx := context.Background()
	client := startArchiverMinIO(ctx, t)
	db := newArchiverDB(t)
	cfg := defaultArchiverTestConfig(client.GetBucket())
	archiver := newTestArchiver(db.Pool, client, cfg)

	sessionID, _ := createArchiverFixtureSession(t, ctx, db.Pool, session.StatusDone, time.Now(), 3)

	archived, err := archiver.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, archived)

	idx, err := archiver.store.Archive().Get(ctx, sessionID)
	require.NoError(t, err)
	require.Nil(t, idx, "a session still inside the TTL window must not be archived")
	require.Equal(t, 3, hotRowCount(t, ctx, db.Pool, sessionID))
}

// TestArchiver_RunOnce_NonTerminalSessionOlderThanTTL_Untouched proves a
// non-terminal session is never selected, no matter how old updated_at is
// -- Status.IsTerminal() gates selection, not age alone.
func TestArchiver_RunOnce_NonTerminalSessionOlderThanTTL_Untouched(t *testing.T) {
	ctx := context.Background()
	client := startArchiverMinIO(ctx, t)
	db := newArchiverDB(t)
	cfg := defaultArchiverTestConfig(client.GetBucket())
	archiver := newTestArchiver(db.Pool, client, cfg)

	sessionID, _ := createArchiverFixtureSession(t, ctx, db.Pool, session.StatusRunning, time.Now().Add(-2*cfg.TranscriptTTL), 3)

	archived, err := archiver.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, archived)

	idx, err := archiver.store.Archive().Get(ctx, sessionID)
	require.NoError(t, err)
	require.Nil(t, idx, "a non-terminal session must never be archived regardless of age")
	require.Equal(t, 3, hotRowCount(t, ctx, db.Pool, sessionID))
}

// TestArchiver_RunOnce_CrashAfterUploadBeforeIndexWrite_RecoversOnRerun
// reproduces write-order step 3 (upload) having completed and the process
// then crashing before step 5 (index-row commit): no transcript_archive
// row exists yet, but the object is already sitting at the deterministic
// key. It proves the crash-safety contract's first case (issue's Testing
// section): hot rows are still intact at that point (nothing is ever
// trimmed before an index row is committed), and a re-run (a plain RunOnce
// call, exactly like the next scheduled scan) completes cleanly --
// re-uploading the same deterministic key is a no-op, not a duplicate or
// an error.
func TestArchiver_RunOnce_CrashAfterUploadBeforeIndexWrite_RecoversOnRerun(t *testing.T) {
	ctx := context.Background()
	client := startArchiverMinIO(ctx, t)
	db := newArchiverDB(t)
	cfg := defaultArchiverTestConfig(client.GetBucket())
	archiver := newTestArchiver(db.Pool, client, cfg)

	sessionID, want := createArchiverFixtureSession(t, ctx, db.Pool, session.StatusDone, time.Now().Add(-2*cfg.TranscriptTTL), 4)

	// Reproduce "crashed after step 3, before step 5" directly: run
	// write-order steps 1-3 (page, encode, upload) via the same private
	// helpers archiveSession itself uses, and deliberately stop there --
	// no Archive().Put.
	evs, err := archiver.pageWholeHotTranscript(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, evs, len(want))
	body, err := encodeArchiveObject(evs)
	require.NoError(t, err)
	key := archiveObjectKey(sessionID)
	_, err = archiver.s3.Upload(ctx, key, body, &s3.UploadOptions{ContentType: "application/gzip", ContentEncoding: "gzip"})
	require.NoError(t, err)

	// Precondition: no index row yet, and the hot rows must still be
	// completely intact -- a crash before the index-row commit must never
	// have touched the hot tier.
	idx, err := archiver.store.Archive().Get(ctx, sessionID)
	require.NoError(t, err)
	require.Nil(t, idx, "precondition: no transcript_archive row yet")
	require.Equal(t, len(want), hotRowCount(t, ctx, db.Pool, sessionID), "precondition: hot rows must be untouched before any index row is committed")

	archived, err := archiver.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, archived, "a re-run must complete cleanly, not error out on the pre-existing object")

	idx, err = archiver.store.Archive().Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, idx)
	require.Equal(t, int64(len(want)), idx.EventCount)
	require.NotNil(t, idx.HotTrimmedAt)
	assertArchivedEventsEqual(t, ctx, client, key, want)
	require.Equal(t, 0, hotRowCount(t, ctx, db.Pool, sessionID))
}

// TestArchiver_RunOnce_CrashAfterIndexWriteBeforeTrim_TrimsWithoutReupload
// reproduces write-order step 5 (index-row commit) having completed and
// the process then crashing before step 6 (trim): a transcript_archive row
// exists with hot_trimmed_at still NULL, and the hot rows are still
// present. It proves the crash-safety contract's second case: a re-run
// trims the hot tier and does not re-upload a second object -- the
// archived object's bytes are byte-identical before and after the re-run.
func TestArchiver_RunOnce_CrashAfterIndexWriteBeforeTrim_TrimsWithoutReupload(t *testing.T) {
	ctx := context.Background()
	client := startArchiverMinIO(ctx, t)
	db := newArchiverDB(t)
	cfg := defaultArchiverTestConfig(client.GetBucket())
	archiver := newTestArchiver(db.Pool, client, cfg)

	sessionID, want := createArchiverFixtureSession(t, ctx, db.Pool, session.StatusDone, time.Now().Add(-2*cfg.TranscriptTTL), 4)

	// Reproduce "crashed after step 5, before step 6": run write-order
	// steps 1-5 by hand (page, encode, upload, commit index), leaving the
	// hot rows untouched -- exactly what a crash right after the index
	// commit would leave behind.
	evs, err := archiver.pageWholeHotTranscript(ctx, sessionID)
	require.NoError(t, err)
	body, err := encodeArchiveObject(evs)
	require.NoError(t, err)
	key := archiveObjectKey(sessionID)
	_, err = archiver.s3.Upload(ctx, key, body, &s3.UploadOptions{ContentType: "application/gzip", ContentEncoding: "gzip"})
	require.NoError(t, err)
	committed := session.ArchiveIndex{
		SessionID:  sessionID,
		S3Bucket:   client.GetBucket(),
		S3Key:      key,
		EventCount: int64(len(evs)),
		MinSeq:     evs[0].Seq,
		MaxSeq:     evs[len(evs)-1].Seq,
	}
	require.NoError(t, archiver.store.Archive().Put(ctx, committed))

	// Precondition: index row exists but hot_trimmed_at is NULL, and hot
	// rows are still present -- trim has not happened yet.
	idx, err := archiver.store.Archive().Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, idx)
	require.Nil(t, idx.HotTrimmedAt, "precondition: not yet trimmed")
	require.Equal(t, len(want), hotRowCount(t, ctx, db.Pool, sessionID), "precondition: hot rows must still be present before trim")

	beforeBytes, err := client.Download(ctx, key)
	require.NoError(t, err)

	archived, err := archiver.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, archived)

	idx, err = archiver.store.Archive().Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, idx.HotTrimmedAt, "re-run must trim the hot tier")
	require.Equal(t, 0, hotRowCount(t, ctx, db.Pool, sessionID))

	afterBytes, err := client.Download(ctx, key)
	require.NoError(t, err)
	require.Equal(t, beforeBytes, afterBytes, "re-run must not re-upload a second object -- bytes must be unchanged")
}

// TestArchiver_RunOnce_AlreadyArchivedSession_IsNoOp proves re-running the
// archiver over an already-fully-archived session (object uploaded, index
// committed, hot tier trimmed) is a no-op: ListArchiveEligible no longer
// selects it, so a second RunOnce call archives nothing further and leaves
// the archive row and object untouched.
func TestArchiver_RunOnce_AlreadyArchivedSession_IsNoOp(t *testing.T) {
	ctx := context.Background()
	client := startArchiverMinIO(ctx, t)
	db := newArchiverDB(t)
	cfg := defaultArchiverTestConfig(client.GetBucket())
	archiver := newTestArchiver(db.Pool, client, cfg)

	sessionID, want := createArchiverFixtureSession(t, ctx, db.Pool, session.StatusDone, time.Now().Add(-2*cfg.TranscriptTTL), 4)

	archived, err := archiver.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, archived)

	idxBefore, err := archiver.store.Archive().Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, idxBefore)

	archived, err = archiver.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, archived, "an already-archived session must not be re-selected")

	idxAfter, err := archiver.store.Archive().Get(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, idxBefore.ArchivedAt, idxAfter.ArchivedAt, "the index row must be untouched by the no-op re-run")
	assertArchivedEventsEqual(t, ctx, client, archiveObjectKey(sessionID), want)
}
