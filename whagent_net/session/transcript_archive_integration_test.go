//go:build integration

// Issue #2240's Testing section, FR8/LB1: proves TranscriptStore.Read is
// genuinely tier-transparent against a real cold-tier object -- not just
// against transcript.go's own unit-level assumptions about what
// transcript_archive and S3 contain. Same real-MinIO-via-testcontainers-go
// precedent as //libs/go/s3:s3_integration_test (see that file's doc
// comment); the Postgres side reuses store_integration_test.go's newDB so
// migration 002 (transcript_archive) is applied from the package's own
// real embedded schema, not a hand-copied one.
//
// Run it explicitly (requires a working Docker daemon), alongside the rest
// of the package's manual integration suite:
//
//	bazel test //whagent_net/session:session_integration_test --test_output=all
package session_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/session"
)

const (
	archiveTestMinioUser = "minioadmin"
	archiveTestMinioPass = "minioadmin"
	archiveTestBucket    = "whagent-transcript-archive-test"
	archiveTestReadLimit = 100 // larger than any fixture's event count -- "get everything" for a positive, production-shaped limit (Read's doc comment: limit <= 0 deliberately never consults the archive, so tests exercising hydration always pass a real page size, matching every real caller -- see api/handlers/session.go's ReadTranscript and worker/context.go).
)

// startArchiveMinIO starts a throwaway MinIO container (matching
// libs/go/s3/s3_integration_test.go's startMinIO precedent) and returns a
// ready *s3.Client whose bucket already exists. s3.Client has no
// CreateBucket method of its own (real callers never provision buckets at
// runtime), so bucket creation goes through the raw AWS SDK directly,
// scoped to this helper only.
func startArchiveMinIO(ctx context.Context, t *testing.T) *s3.Client {
	t.Helper()

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "minio/minio:latest",
			ExposedPorts: []string{"9000/tcp"},
			Env: map[string]string{
				"MINIO_ROOT_USER":     archiveTestMinioUser,
				"MINIO_ROOT_PASSWORD": archiveTestMinioPass,
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
			return aws.Credentials{AccessKeyID: archiveTestMinioUser, SecretAccessKey: archiveTestMinioPass}, nil
		})),
	)
	require.NoError(t, err, "load aws config")
	raw := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	_, err = raw.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(archiveTestBucket)})
	require.NoError(t, err, "create test bucket")

	client, err := s3.NewClient(ctx, s3.Config{
		Bucket:    archiveTestBucket,
		Region:    "us-east-1",
		Endpoint:  endpoint,
		AccessKey: archiveTestMinioUser,
		SecretKey: archiveTestMinioPass,
	})
	require.NoError(t, err, "s3.NewClient")
	return client
}

// archiveObjectKey mirrors the cold-object contract's key format
// (ARCHITECTURE.md "Transcript storage tiers", ArchiveIndex's doc
// comment): "sessions/{session_id}.jsonl.gz".
func archiveObjectKey(sessionID uuid.UUID) string {
	return fmt.Sprintf("sessions/%s.jsonl.gz", sessionID)
}

// uploadArchiveObject writes evs to client at sessionID's documented key as
// gzipped JSON Lines, one events.Event per line -- exactly the format
// decodeArchiveObject (transcript.go) expects to read back.
func uploadArchiveObject(t *testing.T, ctx context.Context, client *s3.Client, sessionID uuid.UUID, evs []events.Event) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, ev := range evs {
		line, err := json.Marshal(ev)
		require.NoError(t, err)
		_, err = gz.Write(line)
		require.NoError(t, err)
		_, err = gz.Write([]byte("\n"))
		require.NoError(t, err)
	}
	require.NoError(t, gz.Close())

	_, err := client.Upload(ctx, archiveObjectKey(sessionID), buf.Bytes(), &s3.UploadOptions{ContentType: "application/gzip"})
	require.NoError(t, err, "upload fixture archive object")
}

// deleteHotRows deletes sessionID's transcript_event rows with seq <=
// throughSeq (throughSeq < 0 deletes every row), simulating FR7's archiver
// having trimmed the hot tier after archiving to S3.
func deleteHotRows(t *testing.T, ctx context.Context, db *dbtest.Postgres, sessionID uuid.UUID, throughSeq int64) {
	t.Helper()
	var err error
	if throughSeq < 0 {
		_, err = db.Pool.Exec(ctx, `DELETE FROM transcript_event WHERE session_id = $1`, sessionID)
	} else {
		_, err = db.Pool.Exec(ctx, `DELETE FROM transcript_event WHERE session_id = $1 AND seq <= $2`, sessionID, throughSeq)
	}
	require.NoError(t, err, "delete hot rows")
}

// assertEventsEqual compares want/got field by field rather than via a
// single require.Equal(t, want, got) on the whole slice: CommittedAt
// round-trips through the cold object's JSON encoding, and time.Time
// values that represent the identical instant are not always
// reflect.DeepEqual (the comparison require.Equal falls back to) once a
// Location pointer or monotonic reading differs, which is a red herring
// this test has no interest in -- Equal (wall-clock instant comparison) is
// what actually matters here, matching LB1's "same content" guarantee.
func assertEventsEqual(t *testing.T, want, got []events.Event, msgAndArgs ...interface{}) {
	t.Helper()
	require.Len(t, got, len(want), msgAndArgs...)
	for i := range want {
		require.Equal(t, want[i].EventID, got[i].EventID, "event[%d].EventID", i)
		require.Equal(t, want[i].SessionID, got[i].SessionID, "event[%d].SessionID", i)
		require.Equal(t, want[i].Seq, got[i].Seq, "event[%d].Seq", i)
		require.Equal(t, want[i].Turn, got[i].Turn, "event[%d].Turn", i)
		require.Equal(t, want[i].Type, got[i].Type, "event[%d].Type", i)
		require.JSONEq(t, string(want[i].Payload), string(got[i].Payload), "event[%d].Payload", i)
		require.True(t, want[i].CommittedAt.Equal(got[i].CommittedAt), "event[%d].CommittedAt: want %s, got %s", i, want[i].CommittedAt, got[i].CommittedAt)
	}
}

// appendFixtureEvents appends n events to sess via s, each with a distinct
// payload ({"i":<index>}) so a later equality assertion actually
// distinguishes events rather than trivially passing on identical rows.
func appendFixtureEvents(t *testing.T, ctx context.Context, s *session.Store, sessionID uuid.UUID, n int) []events.Event {
	t.Helper()
	evs := make([]events.Event, 0, n)
	for i := 0; i < n; i++ {
		payload := json.RawMessage(fmt.Sprintf(`{"i":%d}`, i))
		ev, err := s.Transcript().Append(ctx, sessionID, 0, "test.archive_event", payload)
		require.NoError(t, err)
		evs = append(evs, ev)
	}
	return evs
}

// TestTranscriptStore_Read_ArchivedSession_MatchesPreDeletionHotRead is the
// issue's headline Testing case: with hot rows deleted and a fixture
// object uploaded at the documented key, Read(from_seq=0, ...) returns the
// identical event sequence the hot rows returned before deletion --
// asserted against the pre-deletion result itself, not hand-written
// expectations.
func TestTranscriptStore_Read_ArchivedSession_MatchesPreDeletionHotRead(t *testing.T) {
	ctx := context.Background()
	client := startArchiveMinIO(ctx, t)

	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)
	want := appendFixtureEvents(t, ctx, s, sess.SessionID, 10)

	// Confirm the pre-deletion hot read really is what we think it is
	// before destroying the hot tier -- otherwise "matches pre-deletion
	// read" would be trivially true against a wrong fixture.
	preDeletion, err := s.Transcript().Read(ctx, sess.SessionID, 0, archiveTestReadLimit)
	require.NoError(t, err)
	assertEventsEqual(t, want, preDeletion, "sanity: hot read before archiving must match what was appended")

	uploadArchiveObject(t, ctx, client, sess.SessionID, want)
	require.NoError(t, s.Archive().Put(ctx, session.ArchiveIndex{
		SessionID:  sess.SessionID,
		S3Bucket:   archiveTestBucket,
		S3Key:      archiveObjectKey(sess.SessionID),
		EventCount: int64(len(want)),
		MinSeq:     want[0].Seq,
		MaxSeq:     want[len(want)-1].Seq,
	}))
	deleteHotRows(t, ctx, db, sess.SessionID, -1)

	sWithS3 := session.New(db.Pool, nil, session.WithS3(client))
	got, err := sWithS3.Transcript().Read(ctx, sess.SessionID, 0, archiveTestReadLimit)
	require.NoError(t, err)
	assertEventsEqual(t, want, got, "archived read must return identical content to the pre-deletion hot read")
}

// TestTranscriptStore_Read_ArchivedSession_PagesAcrossBoundary proves
// from_seq in the middle of a fully-archived (hot rows deleted) transcript
// returns the correct window, and that the caller-computed next_from_seq
// (the same "last returned seq + 1" rule api/handlers/session.go's
// ReadTranscript uses) resumes exactly after the last event returned --
// this store package has no ReadTranscript of its own, so the test applies
// that rule directly to Read's result.
func TestTranscriptStore_Read_ArchivedSession_PagesAcrossBoundary(t *testing.T) {
	ctx := context.Background()
	client := startArchiveMinIO(ctx, t)

	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)
	all := appendFixtureEvents(t, ctx, s, sess.SessionID, 10)

	uploadArchiveObject(t, ctx, client, sess.SessionID, all)
	require.NoError(t, s.Archive().Put(ctx, session.ArchiveIndex{
		SessionID:  sess.SessionID,
		S3Bucket:   archiveTestBucket,
		S3Key:      archiveObjectKey(sess.SessionID),
		EventCount: int64(len(all)),
		MinSeq:     all[0].Seq,
		MaxSeq:     all[len(all)-1].Seq,
	}))
	deleteHotRows(t, ctx, db, sess.SessionID, -1)

	sWithS3 := session.New(db.Pool, nil, session.WithS3(client))

	// all[] is 0-indexed but Seq starts at 1; from_seq=5 with limit=3 asks
	// for the middle of the archived transcript.
	page, err := sWithS3.Transcript().Read(ctx, sess.SessionID, 5, 3)
	require.NoError(t, err)
	assertEventsEqual(t, all[4:7], page, "middle page of an archived transcript")

	nextFromSeq := int64(5)
	for _, ev := range page {
		if ev.Seq >= nextFromSeq {
			nextFromSeq = ev.Seq + 1
		}
	}
	require.Equal(t, all[6].Seq+1, nextFromSeq, "next_from_seq must resume exactly after the last event in the page")

	// Resuming from nextFromSeq must pick up exactly where the first page
	// left off, with no gap and no duplicate.
	rest, err := sWithS3.Transcript().Read(ctx, sess.SessionID, nextFromSeq, archiveTestReadLimit)
	require.NoError(t, err)
	assertEventsEqual(t, all[7:], rest, "resuming from next_from_seq must continue with no gap or duplicate")
}

// TestTranscriptStore_Read_MixedTiers_MergesAscendingNoDuplicates proves
// the "hot rows for seq > N, archived object covering seq <= N" case
// (archived but not yet trimmed past N) returns one ascending,
// duplicate-free sequence -- mergeTiers' core contract exercised against a
// real download/decode, not just in-process slices.
func TestTranscriptStore_Read_MixedTiers_MergesAscendingNoDuplicates(t *testing.T) {
	ctx := context.Background()
	client := startArchiveMinIO(ctx, t)

	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)
	all := appendFixtureEvents(t, ctx, s, sess.SessionID, 10)

	const boundary = int64(6) // cold covers seq <= 6, hot keeps seq > 6

	// Archive the whole transcript (FR7 always archives everything it has
	// at archive time) but only trim hot rows through the boundary --
	// exactly "archived but not yet fully trimmed".
	uploadArchiveObject(t, ctx, client, sess.SessionID, all)
	require.NoError(t, s.Archive().Put(ctx, session.ArchiveIndex{
		SessionID:  sess.SessionID,
		S3Bucket:   archiveTestBucket,
		S3Key:      archiveObjectKey(sess.SessionID),
		EventCount: int64(len(all)),
		MinSeq:     all[0].Seq,
		MaxSeq:     all[len(all)-1].Seq,
	}))
	deleteHotRows(t, ctx, db, sess.SessionID, boundary)

	sWithS3 := session.New(db.Pool, nil, session.WithS3(client))
	got, err := sWithS3.Transcript().Read(ctx, sess.SessionID, 0, archiveTestReadLimit)
	require.NoError(t, err)
	assertEventsEqual(t, all, got, "mixed hot+cold read must merge into the original ascending, duplicate-free sequence")

	seen := make(map[int64]bool, len(got))
	for _, ev := range got {
		require.False(t, seen[ev.Seq], "seq %d must not be emitted twice across tiers", ev.Seq)
		seen[ev.Seq] = true
	}
}

// TestTranscriptStore_Read_NoArchiveNoHotRows_ReturnsEmptyNotError proves a
// session with neither hot rows nor an archive row returns an empty
// result, not an error (issue's Testing section, fourth bullet).
func TestTranscriptStore_Read_NoArchiveNoHotRows_ReturnsEmptyNotError(t *testing.T) {
	ctx := context.Background()
	client := startArchiveMinIO(ctx, t)

	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)

	sWithS3 := session.New(db.Pool, nil, session.WithS3(client))
	got, err := sWithS3.Transcript().Read(ctx, sess.SessionID, 0, archiveTestReadLimit)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestTranscriptStore_Read_ArchiveRowWithMissingObject_ReturnsError proves
// an archive row that promises an S3 object which does not actually exist
// surfaces as an error rather than a silently truncated transcript (issue's
// Testing section, fourth bullet).
func TestTranscriptStore_Read_ArchiveRowWithMissingObject_ReturnsError(t *testing.T) {
	ctx := context.Background()
	client := startArchiveMinIO(ctx, t)

	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)
	// Fewer hot rows than the read limit, so Read must consult the archive
	// row instead of short-circuiting on a full hot page.
	appendFixtureEvents(t, ctx, s, sess.SessionID, 2)

	require.NoError(t, s.Archive().Put(ctx, session.ArchiveIndex{
		SessionID:  sess.SessionID,
		S3Bucket:   archiveTestBucket,
		S3Key:      archiveObjectKey(sess.SessionID), // never uploaded
		EventCount: 2,
		MinSeq:     1,
		MaxSeq:     2,
	}))

	sWithS3 := session.New(db.Pool, nil, session.WithS3(client))
	_, err := sWithS3.Transcript().Read(ctx, sess.SessionID, 0, archiveTestReadLimit)
	require.Error(t, err, "an archive row pointing at a missing object must surface as an error, never a silently truncated transcript")
}
