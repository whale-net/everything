package rmq

import manman "github.com/whale-net/everything/manmanv2/models"

// BuildBackupCommand builds the BackupCommand payload shared by the manual
// (TriggerBackup) and scheduled (Activities.DispatchBackup) dispatch paths,
// so a new field only needs to be wired up once. CreatedAt is intentionally
// left unset -- callers that need it set (the manual path) set it themselves
// after calling this, matching each path's existing behavior.
func BuildBackupCommand(backup *manman.Backup, cfg *manman.BackupConfig, volume *manman.GameConfigVolume, s3Key, presignedURL string, preActionCommands []string) *BackupCommand {
	hostPath := ""
	if volume.HostSubpath != nil {
		hostPath = *volume.HostSubpath
	}

	return &BackupCommand{
		BackupID:          backup.BackupID,
		SGCID:             backup.ServerGameConfigID,
		VolumeType:        volume.VolumeType,
		VolumeHostPath:    hostPath,
		VolumeName:        volume.Name,
		BackupPath:        cfg.BackupPath,
		S3Key:             s3Key,
		PresignedURL:      presignedURL,
		PreActionCommands: preActionCommands,
	}
}
