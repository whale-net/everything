// No-evaluator/no-scheduler structural coverage for FR58's explicit scope
// boundary ("V1 stores and renders bands only -- no evaluation, no
// notification") -- this task's Testing section: "Grep for any scheduler
// or evaluator introduced by this task and assert there is none." No
// build tag -- needs no database, so it runs in every plain
// `bazel test //leaflab/...`. plant_type_bands.go's contents are embedded
// at compile time (go:embed) rather than read from the working directory
// at run time, mirroring leaflab/api/readings/source_analysis_test.go's
// own go:embed pattern.
package main

import (
	_ "embed"
	"regexp"
	"strings"
	"testing"
)

// forbiddenEvaluatorPatterns match actual code constructs an
// evaluator/scheduler/notifier would need to exist at all.
var forbiddenEvaluatorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)INSERT\s+INTO\s+alert\b`),
	regexp.MustCompile(`(?i)INSERT\s+INTO\s+notification\b`),
	regexp.MustCompile(`(?i)\bfunc\s+\w*[Ee]valuat\w*\(`),
	regexp.MustCompile(`(?i)\bfunc\s+\w*[Ss]chedul\w*\(`),
	regexp.MustCompile(`(?i)\bfunc\s+\w*[Nn]otify\w*\(`),
	regexp.MustCompile(`(?i)\bfunc\s+\w*[Ff]ire\w*\(`),
	regexp.MustCompile(`\btime\.NewTicker\b`),
	regexp.MustCompile(`(?i)\bcron\.`),
}

//go:embed plant_type_bands.go
var plantTypeBandsSource string

// TestPlantTypeBandsFile_NoEvaluatorSchedulerOrNotificationCode proves
// FR58's "stores and renders bands only" scope boundary at the source
// level for the file this task adds: SetPlantTypeBands/GetPlantTypeBands
// store and read a band set verbatim, and this file introduces no
// evaluator, scheduler, alert table write or notification dispatch.
//
// forbiddenEvaluatorPatterns match actual code constructs an
// evaluator/scheduler/notifier would need to exist at all -- not the bare
// words "evaluator"/"notification"/"alert", which this file's own doc
// comments legitimately use in prose to describe what is deliberately
// absent (see plant_type_bands.go's package-level doc comment). Comment
// lines are excluded before these patterns run, so this test cannot be
// defeated (or falsely tripped) by prose alone.
func TestPlantTypeBandsFile_NoEvaluatorSchedulerOrNotificationCode(t *testing.T) {
	if plantTypeBandsSource == "" {
		t.Fatal("test setup: embedded plant_type_bands.go source is empty")
	}
	assertNoEvaluatorSchedulerOrNotificationCode(t, "plant_type_bands.go", plantTypeBandsSource)
}

func assertNoEvaluatorSchedulerOrNotificationCode(t *testing.T, filename, source string) {
	t.Helper()
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		for _, re := range forbiddenEvaluatorPatterns {
			if re.MatchString(line) {
				t.Errorf("%s: line matches forbidden evaluator/scheduler/notification pattern %s: %q", filename, re.String(), trimmed)
			}
		}
	}
}
