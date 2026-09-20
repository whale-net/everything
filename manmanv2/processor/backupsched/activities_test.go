package backupsched

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/api/repository"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
	manman "github.com/whale-net/everything/manmanv2/models"
)

// The fakes below embed the real repository interfaces (nil-method-set) and
// override only what DispatchBackup's happy path touches -- the same
// pattern manmanv2/api/handlers/servergameconfig_test.go's MockSGCCreateRepo
// uses, so an accidental call to an unimplemented method panics loudly
// instead of silently returning a zero value.

type fakeBackupConfigRepo struct {
	repository.BackupConfigRepository
	cfg *manman.BackupConfig
}

func (f *fakeBackupConfigRepo) Get(ctx context.Context, backupConfigID int64) (*manman.BackupConfig, error) {
	return f.cfg, nil
}

// ListActions returns no attached actions: DispatchBackup's actions loop is
// then a no-op, so the wire-shape test below never needs to exercise
// Activities.ActionRepo (a concrete *postgres.ActionRepository, left nil).
func (f *fakeBackupConfigRepo) ListActions(ctx context.Context, backupConfigID int64) ([]*manman.BackupConfigAction, error) {
	return nil, nil
}

type fakeVolumeRepo struct {
	repository.GameConfigVolumeRepository
	volume *manman.GameConfigVolume
}

func (f *fakeVolumeRepo) Get(ctx context.Context, volumeID int64) (*manman.GameConfigVolume, error) {
	return f.volume, nil
}

type fakeSGCRepo struct {
	repository.ServerGameConfigRepository
	sgcs []*manman.ServerGameConfig
}

func (f *fakeSGCRepo) List(ctx context.Context, serverID *int64, limit, offset int) ([]*manman.ServerGameConfig, error) {
	return f.sgcs, nil
}

type fakeSessionRepo struct {
	repository.SessionRepository
	bySGC map[int64][]*manman.Session
}

func (f *fakeSessionRepo) List(ctx context.Context, sgcID *int64, limit, offset int) ([]*manman.Session, error) {
	if sgcID == nil {
		return nil, nil
	}
	return f.bySGC[*sgcID], nil
}

type fakeServerRepo struct {
	repository.ServerRepository
	byID map[int64]*manman.Server
}

func (f *fakeServerRepo) Get(ctx context.Context, serverID int64) (*manman.Server, error) {
	return f.byID[serverID], nil
}

// backupsCreated records every Backup Create call so the wire-shape test can
// assert the record shape (Status, TriggerSource, and the foreign keys) that
// FR17/NFR5 require to stay unchanged.
type fakeBackupRepo struct {
	repository.BackupRepository
	nextID  int64
	created []*manman.Backup
}

func (f *fakeBackupRepo) Create(ctx context.Context, backup *manman.Backup) (*manman.Backup, error) {
	f.nextID++
	backup.BackupID = f.nextID
	f.created = append(f.created, backup)
	return backup, nil
}

func (f *fakeBackupRepo) UpdateStatus(ctx context.Context, backupID int64, status string, s3URL *string, sizeBytes *int64, errMsg *string) error {
	return nil
}

// publishedMessage is one fakePublisher.Publish call, captured verbatim so
// the wire-shape test can marshal body itself and compare against a literal
// expected JSON string.
type publishedMessage struct {
	exchange   string
	routingKey string
	body       interface{}
}

type fakePublisher struct {
	calls []publishedMessage
}

func (f *fakePublisher) Publish(ctx context.Context, exchange, routingKey string, body interface{}) error {
	f.calls = append(f.calls, publishedMessage{exchange: exchange, routingKey: routingKey, body: body})
	return nil
}

// presignCall is one fakePresigner.PresignPutURL call.
type presignCall struct {
	key string
	ttl time.Duration
}

type fakePresigner struct {
	url   string
	calls []presignCall
}

func (f *fakePresigner) PresignPutURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	f.calls = append(f.calls, presignCall{key: key, ttl: ttl})
	return f.url, nil
}

// dispatchFixture wires one BackupConfig -> Volume -> SGC -> Session/Server
// chain, the minimal shape DispatchBackup needs to reach the publish step.
type dispatchFixture struct {
	backupConfigID int64
	volumeID       int64
	sgcID          int64
	sessionID      int64
	serverID       int64
	gameConfigID   int64

	backupConfigs *fakeBackupConfigRepo
	volumes       *fakeVolumeRepo
	sgcs          *fakeSGCRepo
	sessions      *fakeSessionRepo
	servers       *fakeServerRepo
	backups       *fakeBackupRepo
	publisher     *fakePublisher
	presigner     *fakePresigner
}

func newDispatchFixture() *dispatchFixture {
	f := &dispatchFixture{
		backupConfigID: 501,
		volumeID:       601,
		sgcID:          701,
		sessionID:      801,
		serverID:       901,
		gameConfigID:   301,
	}

	hostSubpath := "saves"
	f.backupConfigs = &fakeBackupConfigRepo{cfg: &manman.BackupConfig{
		BackupConfigID: f.backupConfigID,
		VolumeID:       f.volumeID,
		CadenceMinutes: 60,
		BackupPath:     "world/saves",
		Enabled:        true,
	}}
	f.volumes = &fakeVolumeRepo{volume: &manman.GameConfigVolume{
		VolumeID:      f.volumeID,
		ConfigID:      f.gameConfigID,
		Name:          "world-data",
		HostSubpath:   &hostSubpath,
		VolumeType:    "bind",
		ContainerPath: "/data",
	}}
	f.sgcs = &fakeSGCRepo{sgcs: []*manman.ServerGameConfig{
		{SGCID: f.sgcID, ServerID: f.serverID, GameConfigID: f.gameConfigID, Status: "running"},
	}}
	f.sessions = &fakeSessionRepo{bySGC: map[int64][]*manman.Session{
		f.sgcID: {{SessionID: f.sessionID, SGCID: f.sgcID, Status: "running"}},
	}}
	f.servers = &fakeServerRepo{byID: map[int64]*manman.Server{
		f.serverID: {ServerID: f.serverID, Name: "host-1"},
	}}
	f.backups = &fakeBackupRepo{}
	f.publisher = &fakePublisher{}
	f.presigner = &fakePresigner{url: "https://s3.example.com/presigned-put"}
	return f
}

func (f *dispatchFixture) activities() *Activities {
	return &Activities{
		Repo: &repository.Repository{
			BackupConfigs:     f.backupConfigs,
			GameConfigVolumes: f.volumes,
			ServerGameConfigs: f.sgcs,
			Sessions:          f.sessions,
			Servers:           f.servers,
			Backups:           f.backups,
		},
		ActionRepo: nil, // never dereferenced: fakeBackupConfigRepo.ListActions returns none
		Publisher:  f.publisher,
		S3Client:   f.presigner,
	}
}

// TestDispatchBackup_WireShape is the FR17 wire-shape test: it drives
// DispatchBackup against fakes and asserts the published routing key and
// the marshalled BackupCommand JSON are byte-identical to what River's
// scheduledBackupWorker.Work (manmanv2/processor/backup_scheduler.go)
// produces for the same inputs. That worker builds the identical
// hostrmq.BackupCommand literal (same fields, same order, same S3 key and
// routing key formats) and this package's DispatchBackup is a verbatim port
// of it -- see activities.go's doc comment. Comparing against a literal
// JSON string (not a struct built with the same type) means an added,
// renamed, or reordered BackupCommand field changes the actual output but
// not this literal, so the test goes red rather than passing vacuously.
func TestDispatchBackup_WireShape(t *testing.T) {
	f := newDispatchFixture()
	a := f.activities()

	if err := a.DispatchBackup(context.Background(), f.backupConfigID); err != nil {
		t.Fatalf("DispatchBackup: %v", err)
	}

	if len(f.backups.created) != 1 {
		t.Fatalf("Backups.Create calls = %d, want 1", len(f.backups.created))
	}
	backup := f.backups.created[0]
	if backup.SessionID != f.sessionID {
		t.Errorf("backup.SessionID = %d, want %d", backup.SessionID, f.sessionID)
	}
	if backup.ServerGameConfigID != f.sgcID {
		t.Errorf("backup.ServerGameConfigID = %d, want %d", backup.ServerGameConfigID, f.sgcID)
	}
	if backup.BackupConfigID == nil || *backup.BackupConfigID != f.backupConfigID {
		t.Errorf("backup.BackupConfigID = %v, want %d", backup.BackupConfigID, f.backupConfigID)
	}
	if backup.VolumeID == nil || *backup.VolumeID != f.volumeID {
		t.Errorf("backup.VolumeID = %v, want %d", backup.VolumeID, f.volumeID)
	}
	if backup.Status != manman.BackupStatusPending {
		t.Errorf("backup.Status = %q, want %q", backup.Status, manman.BackupStatusPending)
	}
	if backup.TriggerSource != manman.BackupTriggerSourceScheduled {
		t.Errorf("backup.TriggerSource = %q, want %q", backup.TriggerSource, manman.BackupTriggerSourceScheduled)
	}

	wantS3Key := "backups/701/501/1.tar.gz" // backups/<sgc_id>/<backup_config_id>/<backup_id>.tar.gz
	if len(f.presigner.calls) != 1 {
		t.Fatalf("PresignPutURL calls = %d, want 1", len(f.presigner.calls))
	}
	if f.presigner.calls[0].key != wantS3Key {
		t.Errorf("presigned key = %q, want %q", f.presigner.calls[0].key, wantS3Key)
	}

	if len(f.publisher.calls) != 1 {
		t.Fatalf("Publish calls = %d, want 1", len(f.publisher.calls))
	}
	msg := f.publisher.calls[0]
	if msg.exchange != "manman" {
		t.Errorf("exchange = %q, want %q", msg.exchange, "manman")
	}
	wantRoutingKey := "command.host.901.backup" // command.host.<server_id>.backup
	if msg.routingKey != wantRoutingKey {
		t.Errorf("routing key = %q, want %q", msg.routingKey, wantRoutingKey)
	}

	cmd, ok := msg.body.(*hostrmq.BackupCommand)
	if !ok {
		t.Fatalf("published body type = %T, want *hostrmq.BackupCommand", msg.body)
	}
	gotJSON, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal published BackupCommand: %v", err)
	}

	// Literal, field-for-field expectation -- CreatedAt is deliberately left
	// at its Go zero value (never set in DispatchBackup, matching
	// scheduledBackupWorker.Work verbatim).
	wantJSON := `{` +
		`"backup_id":1,` +
		`"sgc_id":701,` +
		`"volume_type":"bind",` +
		`"volume_host_path":"saves",` +
		`"volume_name":"world-data",` +
		`"backup_path":"world/saves",` +
		`"s3_key":"backups/701/501/1.tar.gz",` +
		`"presigned_url":"https://s3.example.com/presigned-put",` +
		`"pre_action_commands":[],` +
		`"created_at":"0001-01-01T00:00:00Z"` +
		`}`
	if string(gotJSON) != wantJSON {
		t.Errorf("published BackupCommand JSON mismatch:\n got:  %s\n want: %s", gotJSON, wantJSON)
	}
}

// TestDispatchBackup_SkipsDisabledConfig proves the documented skip-disabled
// short-circuit: no Backup record is created and nothing is published, so a
// disabled config can never spuriously dispatch.
func TestDispatchBackup_SkipsDisabledConfig(t *testing.T) {
	f := newDispatchFixture()
	f.backupConfigs.cfg.Enabled = false
	a := f.activities()

	if err := a.DispatchBackup(context.Background(), f.backupConfigID); err != nil {
		t.Fatalf("DispatchBackup: %v", err)
	}

	if len(f.backups.created) != 0 {
		t.Errorf("Backups.Create calls = %d, want 0 for a disabled config", len(f.backups.created))
	}
	if len(f.publisher.calls) != 0 {
		t.Errorf("Publish calls = %d, want 0 for a disabled config", len(f.publisher.calls))
	}
}
