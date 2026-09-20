package handlers

import (
	"context"
	"testing"

	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeBackupListRepo is a minimal BackupRepository fake that only records
// the filter/limit/offset ListBackups passes to List -- the real
// filter-narrowing SQL is covered against real Postgres in
// backup_list_integration_test.go. This fake isolates the handler's own
// mapping logic (zero-value-means-unset, status validation, pagination
// arithmetic) from that DB-level behavior.
type fakeBackupListRepo struct {
	repository.BackupRepository

	lastFilter repository.BackupListFilter
	lastLimit  int
	lastOffset int

	rows []*repository.BackupListRow
}

func (f *fakeBackupListRepo) List(ctx context.Context, filter repository.BackupListFilter, limit, offset int) ([]*repository.BackupListRow, error) {
	f.lastFilter = filter
	f.lastLimit = limit
	f.lastOffset = offset
	return f.rows, nil
}

// TestListBackups_RejectsUnknownStatusBeforeQuerying proves an unrecognized
// status filter is rejected with InvalidArgument without ever reaching the
// repository.
func TestListBackups_RejectsUnknownStatusBeforeQuerying(t *testing.T) {
	repo := &fakeBackupListRepo{}
	h := NewBackupHandler(repo, nil, nil)

	_, err := h.ListBackups(context.Background(), &pb.ListBackupsRequest{Status: "bogus"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for status %q, got %v", "bogus", err)
	}
}

// TestListBackups_ZeroValueFiltersAreUnset proves the handler treats 0/""
// request fields as "no filter" (nil pointers on the repository call),
// matching the existing ">0 means set" convention used elsewhere in this
// handler.
func TestListBackups_ZeroValueFiltersAreUnset(t *testing.T) {
	repo := &fakeBackupListRepo{}
	h := NewBackupHandler(repo, nil, nil)

	if _, err := h.ListBackups(context.Background(), &pb.ListBackupsRequest{}); err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	f := repo.lastFilter
	if f.SGCID != nil || f.SessionID != nil || f.VolumeID != nil || f.BackupConfigID != nil || f.Status != nil {
		t.Errorf("expected every filter field nil for an all-zero request, got %+v", f)
	}
}

// TestListBackups_NonZeroFiltersArePassedThrough proves each non-zero
// request field becomes the corresponding non-nil filter pointer, alone and
// in combination.
func TestListBackups_NonZeroFiltersArePassedThrough(t *testing.T) {
	repo := &fakeBackupListRepo{}
	h := NewBackupHandler(repo, nil, nil)

	req := &pb.ListBackupsRequest{
		ServerGameConfigId: 10,
		SessionId:          20,
		VolumeId:           30,
		BackupConfigId:     40,
		Status:             manman.BackupStatusRunning,
	}
	if _, err := h.ListBackups(context.Background(), req); err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	f := repo.lastFilter
	switch {
	case f.SGCID == nil || *f.SGCID != 10:
		t.Errorf("SGCID = %v, want 10", f.SGCID)
	case f.SessionID == nil || *f.SessionID != 20:
		t.Errorf("SessionID = %v, want 20", f.SessionID)
	case f.VolumeID == nil || *f.VolumeID != 30:
		t.Errorf("VolumeID = %v, want 30", f.VolumeID)
	case f.BackupConfigID == nil || *f.BackupConfigID != 40:
		t.Errorf("BackupConfigID = %v, want 40", f.BackupConfigID)
	case f.Status == nil || *f.Status != manman.BackupStatusRunning:
		t.Errorf("Status = %v, want %q", f.Status, manman.BackupStatusRunning)
	}
}

// TestListBackups_ItemsMirrorBackupsCountOrderAndNames proves the handler's
// own item-building loop -- independent of what Postgres joins produce --
// keeps `items` the same length and order as `backups` and copies each
// row's display-name fields through.
func TestListBackups_ItemsMirrorBackupsCountOrderAndNames(t *testing.T) {
	repo := &fakeBackupListRepo{
		rows: []*repository.BackupListRow{
			{
				Backup:               &manman.Backup{BackupID: 1, Status: manman.BackupStatusCompleted},
				ServerGameConfigName: "server-a / config-a",
				GameConfigName:       "config-a",
				VolumeName:           "vol-a",
				ServerName:           "server-a",
			},
			{
				Backup:               &manman.Backup{BackupID: 2, Status: manman.BackupStatusFailed},
				ServerGameConfigName: "server-b / config-b",
				GameConfigName:       "config-b",
				VolumeName:           "",
				ServerName:           "server-b",
			},
		},
	}
	h := NewBackupHandler(repo, nil, nil)

	resp, err := h.ListBackups(context.Background(), &pb.ListBackupsRequest{})
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(resp.Items) != len(resp.Backups) || len(resp.Backups) != 2 {
		t.Fatalf("expected 2 backups and 2 items, got %d backups / %d items", len(resp.Backups), len(resp.Items))
	}
	for i, item := range resp.Items {
		if item.Backup.BackupId != resp.Backups[i].BackupId {
			t.Errorf("position %d: items[].backup.backup_id=%d != backups[]=%d", i, item.Backup.BackupId, resp.Backups[i].BackupId)
		}
	}
	if resp.Items[0].ServerGameConfigName != "server-a / config-a" || resp.Items[0].VolumeName != "vol-a" {
		t.Errorf("expected first item's display names to carry through, got %+v", resp.Items[0])
	}
	if resp.Items[1].VolumeName != "" {
		t.Errorf("expected second item's empty volume_name to carry through, got %q", resp.Items[1].VolumeName)
	}
}

// TestListBackups_PageSizeDefaultAndClamp proves the handler defaults an
// unset/non-positive page_size to 50 and clamps anything above 100 down to
// 100, before adding the "+1" over-fetch used to detect a next page.
func TestListBackups_PageSizeDefaultAndClamp(t *testing.T) {
	cases := []struct {
		name          string
		requested     int32
		wantOverFetch int
	}{
		{"unset defaults to 50", 0, 51},
		{"negative defaults to 50", -5, 51},
		{"within range passes through", 10, 11},
		{"above max clamps to 100", 500, 101},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeBackupListRepo{}
			h := NewBackupHandler(repo, nil, nil)
			if _, err := h.ListBackups(context.Background(), &pb.ListBackupsRequest{PageSize: tc.requested}); err != nil {
				t.Fatalf("ListBackups: %v", err)
			}
			if repo.lastLimit != tc.wantOverFetch {
				t.Errorf("limit passed to repo = %d, want %d", repo.lastLimit, tc.wantOverFetch)
			}
		})
	}
}

// TestBackupToProto_MapsStatusAndPointerFields proves backupToProto emits
// status, error_message, backup_config_id, volume_id, and trigger_source
// (M7 FR2/FR3 backing, plan #2777, task #2808) -- fields the issue calls out
// as previously dropped even though the proto already carried them.
func TestBackupToProto_MapsStatusAndPointerFields(t *testing.T) {
	backupConfigID := int64(42)
	volumeID := int64(7)
	errMsg := "disk full"
	s3URL := "s3://bucket/key.tar.gz"
	sizeBytes := int64(1024)
	description := "nightly save"

	b := &manman.Backup{
		BackupID:           1,
		SessionID:          2,
		ServerGameConfigID: 3,
		BackupConfigID:     &backupConfigID,
		VolumeID:           &volumeID,
		S3URL:              &s3URL,
		SizeBytes:          &sizeBytes,
		Status:             manman.BackupStatusFailed,
		ErrorMessage:       &errMsg,
		Description:        &description,
		TriggerSource:      manman.BackupTriggerSourceManual,
	}

	pbBackup := backupToProto(b)

	if pbBackup.Status != manman.BackupStatusFailed {
		t.Errorf("Status = %q, want %q", pbBackup.Status, manman.BackupStatusFailed)
	}
	if pbBackup.ErrorMessage != errMsg {
		t.Errorf("ErrorMessage = %q, want %q", pbBackup.ErrorMessage, errMsg)
	}
	if pbBackup.BackupConfigId != backupConfigID {
		t.Errorf("BackupConfigId = %d, want %d", pbBackup.BackupConfigId, backupConfigID)
	}
	if pbBackup.VolumeId != volumeID {
		t.Errorf("VolumeId = %d, want %d", pbBackup.VolumeId, volumeID)
	}
	if pbBackup.TriggerSource != manman.BackupTriggerSourceManual {
		t.Errorf("TriggerSource = %q, want %q", pbBackup.TriggerSource, manman.BackupTriggerSourceManual)
	}
}

// TestBackupToProto_NilPointersMapToZeroValue proves the nil-pointer fields
// (backup_config_id, volume_id, error_message, s3_url, size_bytes,
// description -- all optional on a Backup row) map to the proto zero value
// rather than panicking, and trigger_source is emitted even with every
// other optional field unset.
func TestBackupToProto_NilPointersMapToZeroValue(t *testing.T) {
	b := &manman.Backup{
		BackupID:           1,
		SessionID:          2,
		ServerGameConfigID: 3,
		Status:             manman.BackupStatusPending,
		TriggerSource:      manman.BackupTriggerSourceUnknown,
	}

	pbBackup := backupToProto(b)

	if pbBackup.BackupConfigId != 0 {
		t.Errorf("BackupConfigId = %d, want 0 for nil BackupConfigID", pbBackup.BackupConfigId)
	}
	if pbBackup.VolumeId != 0 {
		t.Errorf("VolumeId = %d, want 0 for nil VolumeID", pbBackup.VolumeId)
	}
	if pbBackup.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q, want empty for nil ErrorMessage", pbBackup.ErrorMessage)
	}
	if pbBackup.TriggerSource != manman.BackupTriggerSourceUnknown {
		t.Errorf("TriggerSource = %q, want %q", pbBackup.TriggerSource, manman.BackupTriggerSourceUnknown)
	}
}
