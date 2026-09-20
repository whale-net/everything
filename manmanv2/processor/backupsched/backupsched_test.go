package backupsched

import (
	"testing"
	"time"
)

// TestScaffoldConstants pins the constants Testing/Implementation phases
// build on — DefaultTaskQueue and ScanScheduleID are load-bearing string
// literals (task queue name, Schedule ID) that must not drift silently, and
// ScanInterval must keep matching River's PeriodicInterval(1*time.Minute).
func TestScaffoldConstants(t *testing.T) {
	if DefaultTaskQueue != "manmanv2-processor" {
		t.Errorf("DefaultTaskQueue = %q, want %q", DefaultTaskQueue, "manmanv2-processor")
	}
	if ScanScheduleID != "manmanv2-backup-scan" {
		t.Errorf("ScanScheduleID = %q, want %q", ScanScheduleID, "manmanv2-backup-scan")
	}
	if ScanInterval != time.Minute {
		t.Errorf("ScanInterval = %v, want %v", ScanInterval, time.Minute)
	}
}
