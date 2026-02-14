package kraken

import (
	"testing"
)

func TestCleanupPlanTotalTagDeletions(t *testing.T) {
	plan := &CleanupPlan{
		TagsToDelete: []string{"tag1", "tag2", "tag3"},
	}
	if plan.TotalTagDeletions() != 3 {
		t.Errorf("expected 3, got %d", plan.TotalTagDeletions())
	}
}

func TestCleanupPlanTotalPackageDeletions(t *testing.T) {
	plan := &CleanupPlan{
		PackagesToDelete: map[string][]int{
			"pkg1": {1, 2},
			"pkg2": {3},
		},
	}
	if plan.TotalPackageDeletions() != 3 {
		t.Errorf("expected 3, got %d", plan.TotalPackageDeletions())
	}
}

func TestCleanupPlanTotalReleaseDeletions(t *testing.T) {
	plan := &CleanupPlan{
		ReleasesToDelete: map[string]int{
			"tag1": 100,
			"tag2": 200,
		},
	}
	if plan.TotalReleaseDeletions() != 2 {
		t.Errorf("expected 2, got %d", plan.TotalReleaseDeletions())
	}
}

func TestCleanupPlanIsEmpty(t *testing.T) {
	emptyPlan := &CleanupPlan{
		PackagesToDelete: make(map[string][]int),
		ReleasesToDelete: make(map[string]int),
	}
	if !emptyPlan.IsEmpty() {
		t.Error("expected empty plan")
	}

	nonEmptyPlan := &CleanupPlan{
		TagsToDelete:     []string{"tag1"},
		PackagesToDelete: make(map[string][]int),
		ReleasesToDelete: make(map[string]int),
	}
	if nonEmptyPlan.IsEmpty() {
		t.Error("expected non-empty plan")
	}
}

func TestCleanupResultIsSuccessful(t *testing.T) {
	successResult := &CleanupResult{}
	if !successResult.IsSuccessful() {
		t.Error("expected successful result")
	}

	failResult := &CleanupResult{
		Errors: []string{"some error"},
	}
	if failResult.IsSuccessful() {
		t.Error("expected unsuccessful result")
	}
}

func TestCleanupResultSummary(t *testing.T) {
	result := &CleanupResult{
		TagsDeleted:     []string{"tag1", "tag2"},
		ReleasesDeleted: []string{"tag1"},
		PackagesDeleted: map[string][]int{
			"pkg1": {1, 2},
		},
		DryRun: true,
	}

	summary := result.Summary()
	if summary == "" {
		t.Error("expected non-empty summary")
	}
	// Check key parts of summary
	if !containsStr(summary, "Tags deleted: 2") {
		t.Error("expected tags deleted count in summary")
	}
	if !containsStr(summary, "Releases deleted: 1") {
		t.Error("expected releases deleted count in summary")
	}
	if !containsStr(summary, "Package versions deleted: 2") {
		t.Error("expected package versions deleted count in summary")
	}
	if !containsStr(summary, "Dry run") {
		t.Error("expected dry run indicator in summary")
	}
}

func TestCleanupResultSummaryWithErrors(t *testing.T) {
	result := &CleanupResult{
		Errors:          []string{"error1", "error2"},
		PackagesDeleted: make(map[string][]int),
	}

	summary := result.Summary()
	if !containsStr(summary, "Errors encountered: 2") {
		t.Error("expected errors count in summary")
	}
}

func TestIdentifyTagsToPruneEmptyTags(t *testing.T) {
	toDelete, toKeep := IdentifyTagsToPrune(nil, 2, 14)
	if len(toDelete) != 0 {
		t.Errorf("expected 0 deletions, got %d", len(toDelete))
	}
	if len(toKeep) != 0 {
		t.Errorf("expected 0 keeps, got %d", len(toKeep))
	}
}

func TestIdentifyTagsToPruneInvalidTags(t *testing.T) {
	tags := []string{"not-a-valid-tag", "another-invalid"}
	toDelete, toKeep := IdentifyTagsToPrune(tags, 2, 14)
	if len(toDelete) != 0 {
		t.Errorf("expected 0 deletions for invalid tags, got %d", len(toDelete))
	}
	if len(toKeep) != 0 {
		t.Errorf("expected 0 keeps for invalid tags, got %d", len(toKeep))
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
