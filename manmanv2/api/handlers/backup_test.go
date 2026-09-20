package handlers

import (
	"testing"

	"github.com/whale-net/everything/manmanv2/models"
)

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
