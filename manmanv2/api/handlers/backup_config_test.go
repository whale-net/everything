package handlers

import (
	"context"
	"testing"

	"github.com/whale-net/everything/manmanv2/api/repository"
	manman "github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

// fakeBackupConfigListRepo is a minimal BackupConfigRepository fake for
// ListBackupConfigs (#2810): it applies the same filter semantics as the
// real Postgres query (nil field = no-op, ORDER BY backup_config_id,
// LIMIT/OFFSET) over an in-memory row set, and records the filter/limit
// it was called with so the handler's request-mapping and pagination
// clamp can be asserted independently of the SQL layer.
type fakeBackupConfigListRepo struct {
	repository.BackupConfigRepository
	rows []*repository.BackupConfigListRow

	lastFilter repository.BackupConfigListFilter
	lastLimit  int
	lastOffset int
}

func (f *fakeBackupConfigListRepo) List(ctx context.Context, filter repository.BackupConfigListFilter, limit, offset int) ([]*repository.BackupConfigListRow, error) {
	f.lastFilter = filter
	f.lastLimit = limit
	f.lastOffset = offset

	var matched []*repository.BackupConfigListRow
	for _, r := range f.rows {
		if filter.VolumeID != nil && r.Config.VolumeID != *filter.VolumeID {
			continue
		}
		if filter.GameConfigID != nil && r.GameConfigID != *filter.GameConfigID {
			continue
		}
		if filter.Enabled != nil && r.Config.Enabled != *filter.Enabled {
			continue
		}
		matched = append(matched, r)
	}

	if offset >= len(matched) {
		return nil, nil
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	return matched[offset:end], nil
}

func newFakeRow(id, volumeID, gameConfigID int64, enabled bool) *repository.BackupConfigListRow {
	return &repository.BackupConfigListRow{
		Config: &manman.BackupConfig{
			BackupConfigID: id,
			VolumeID:       volumeID,
			CadenceMinutes: 30,
			BackupPath:     "saves",
			Enabled:        enabled,
		},
		VolumeName:     "vol",
		GameConfigID:   gameConfigID,
		GameConfigName: "cfg",
		GameName:       "game",
	}
}

func TestListBackupConfigs_NoFilterPassesNilFilter(t *testing.T) {
	repo := &fakeBackupConfigListRepo{rows: []*repository.BackupConfigListRow{
		newFakeRow(1, 10, 100, true),
		newFakeRow(2, 20, 200, true),
	}}
	h := &BackupConfigHandler{backupConfigRepo: repo}

	resp, err := h.ListBackupConfigs(context.Background(), &pb.ListBackupConfigsRequest{})
	if err != nil {
		t.Fatalf("ListBackupConfigs: %v", err)
	}
	if repo.lastFilter.VolumeID != nil || repo.lastFilter.GameConfigID != nil || repo.lastFilter.Enabled != nil {
		t.Fatalf("expected an all-nil filter with no request fields set, got %+v", repo.lastFilter)
	}
	if len(resp.Configs) != 2 || len(resp.Items) != 2 {
		t.Fatalf("expected fleet-wide result across both rows, got configs=%d items=%d", len(resp.Configs), len(resp.Items))
	}
}

// TestListBackupConfigs_VolumeIDOnlyMatchesLegacyBehavior guards the Config
// Editor's Volumes tab: volume_id alone must map to a VolumeID-only filter,
// not touch GameConfigID/Enabled.
func TestListBackupConfigs_VolumeIDOnlyMatchesLegacyBehavior(t *testing.T) {
	repo := &fakeBackupConfigListRepo{rows: []*repository.BackupConfigListRow{
		newFakeRow(1, 10, 100, true),
	}}
	h := &BackupConfigHandler{backupConfigRepo: repo}

	_, err := h.ListBackupConfigs(context.Background(), &pb.ListBackupConfigsRequest{VolumeId: 10})
	if err != nil {
		t.Fatalf("ListBackupConfigs: %v", err)
	}
	if repo.lastFilter.VolumeID == nil || *repo.lastFilter.VolumeID != 10 {
		t.Fatalf("expected VolumeID filter = 10, got %+v", repo.lastFilter)
	}
	if repo.lastFilter.GameConfigID != nil || repo.lastFilter.Enabled != nil {
		t.Fatalf("volume_id-only request must not set other filters, got %+v", repo.lastFilter)
	}
}

func TestListBackupConfigs_GameConfigIDFilterNarrows(t *testing.T) {
	repo := &fakeBackupConfigListRepo{rows: []*repository.BackupConfigListRow{
		newFakeRow(1, 10, 100, true),
		newFakeRow(2, 20, 200, true),
	}}
	h := &BackupConfigHandler{backupConfigRepo: repo}

	resp, err := h.ListBackupConfigs(context.Background(), &pb.ListBackupConfigsRequest{GameConfigId: 200})
	if err != nil {
		t.Fatalf("ListBackupConfigs: %v", err)
	}
	if len(resp.Configs) != 1 || resp.Configs[0].BackupConfigId != 2 {
		t.Fatalf("expected only GameConfig 200's config, got %+v", resp.Configs)
	}
}

// TestListBackupConfigs_EnabledFilterDistinguishesFalseFromUnset proves the
// handler forwards Enabled by pointer identity, not by truthiness: an unset
// request field must not be conflated with an explicit `false`.
func TestListBackupConfigs_EnabledFilterDistinguishesFalseFromUnset(t *testing.T) {
	repo := &fakeBackupConfigListRepo{rows: []*repository.BackupConfigListRow{
		newFakeRow(1, 10, 100, true),
		newFakeRow(2, 20, 200, false),
	}}
	h := &BackupConfigHandler{backupConfigRepo: repo}

	// Unset: both returned.
	resp, err := h.ListBackupConfigs(context.Background(), &pb.ListBackupConfigsRequest{})
	if err != nil {
		t.Fatalf("ListBackupConfigs (unset): %v", err)
	}
	if len(resp.Configs) != 2 {
		t.Fatalf("expected both configs with enabled unset, got %d", len(resp.Configs))
	}

	// enabled = false: only the disabled one.
	no := false
	resp, err = h.ListBackupConfigs(context.Background(), &pb.ListBackupConfigsRequest{Enabled: &no})
	if err != nil {
		t.Fatalf("ListBackupConfigs (enabled=false): %v", err)
	}
	if len(resp.Configs) != 1 || resp.Configs[0].BackupConfigId != 2 {
		t.Fatalf("expected only the disabled config, got %+v", resp.Configs)
	}
}

// TestListBackupConfigs_PageSizeClamp mirrors ListBackups: default 50,
// max 100, and the repository is asked for pageSize+1 rows so the handler
// can detect "more pages exist" without a separate count query.
func TestListBackupConfigs_PageSizeClamp(t *testing.T) {
	repo := &fakeBackupConfigListRepo{}
	h := &BackupConfigHandler{backupConfigRepo: repo}

	cases := []struct {
		name      string
		reqSize   int32
		wantLimit int
	}{
		{"zero defaults to 50", 0, 51},
		{"negative defaults to 50", -1, 51},
		{"within range passes through", 10, 11},
		{"over max clamps to 100", 500, 101},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.ListBackupConfigs(context.Background(), &pb.ListBackupConfigsRequest{PageSize: tc.reqSize}); err != nil {
				t.Fatalf("ListBackupConfigs: %v", err)
			}
			if repo.lastLimit != tc.wantLimit {
				t.Fatalf("want repo limit %d, got %d", tc.wantLimit, repo.lastLimit)
			}
		})
	}
}

// TestListBackupConfigs_PaginationTokenRoundTrip proves a full page returns
// a token that resumes correctly, and the last (partial) page returns none.
func TestListBackupConfigs_PaginationTokenRoundTrip(t *testing.T) {
	repo := &fakeBackupConfigListRepo{rows: []*repository.BackupConfigListRow{
		newFakeRow(1, 10, 100, true),
		newFakeRow(2, 10, 100, true),
		newFakeRow(3, 10, 100, true),
	}}
	h := &BackupConfigHandler{backupConfigRepo: repo}

	page1, err := h.ListBackupConfigs(context.Background(), &pb.ListBackupConfigsRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Configs) != 2 || page1.NextPageToken == "" {
		t.Fatalf("expected a full page of 2 with a next_page_token, got %d rows, token=%q", len(page1.Configs), page1.NextPageToken)
	}

	page2, err := h.ListBackupConfigs(context.Background(), &pb.ListBackupConfigsRequest{PageSize: 2, PageToken: page1.NextPageToken})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Configs) != 1 || page2.NextPageToken != "" {
		t.Fatalf("expected the last partial page (1 row, no token), got %d rows, token=%q", len(page2.Configs), page2.NextPageToken)
	}
	if page2.Configs[0].BackupConfigId != 3 {
		t.Fatalf("expected page2 to resume at row 3, got %+v", page2.Configs)
	}
}

// TestListBackupConfigs_ItemsMatchConfigs proves items and configs stay in
// lockstep (same count/order) and items carry the joined display names.
func TestListBackupConfigs_ItemsMatchConfigs(t *testing.T) {
	repo := &fakeBackupConfigListRepo{rows: []*repository.BackupConfigListRow{
		newFakeRow(1, 10, 100, true),
		newFakeRow(2, 20, 200, false),
	}}
	h := &BackupConfigHandler{backupConfigRepo: repo}

	resp, err := h.ListBackupConfigs(context.Background(), &pb.ListBackupConfigsRequest{})
	if err != nil {
		t.Fatalf("ListBackupConfigs: %v", err)
	}
	if len(resp.Items) != len(resp.Configs) {
		t.Fatalf("items/configs count mismatch: %d vs %d", len(resp.Items), len(resp.Configs))
	}
	for i, item := range resp.Items {
		if item.Config.BackupConfigId != resp.Configs[i].BackupConfigId {
			t.Fatalf("items[%d] out of order with configs: %+v vs %+v", i, item.Config, resp.Configs[i])
		}
		if item.VolumeName != "vol" || item.GameConfigName != "cfg" || item.GameName != "game" {
			t.Fatalf("items[%d] missing joined display context: %+v", i, item)
		}
	}
}
