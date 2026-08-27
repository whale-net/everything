package main

// FR57/NFR19 source-analysis check (#1349's Testing section): "the device
// id is reachable in exactly one place -- a support-details block". This
// greps leaflab/ui/components' checked-in *.templ source for the literal
// "device_id" and fails unless exactly one file mentions it -- matching this
// task's Validation criterion verbatim: `grep -rn "device_id"
// leaflab/ui/components/` shows exactly one component. Mirrors
// nfr18_conformance_test.go's grep-over-checked-in-source approach and
// leaflabUIDir's marker-file technique; the "//leaflab/ui/components:
// conformance_srcs" data dependency this needs is already staged by
// BUILD.bazel's ui_test rule for that other check.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeviceID_FR57_RendersInExactlyOneBFFComponent proves the device id
// renders in exactly one component (support_details.templ) and nowhere
// else under leaflab/ui/components -- a future component that names
// device_id directly, instead of going through BoardInfo.display_name,
// fails the build instead of drifting past review unnoticed.
func TestDeviceID_FR57_RendersInExactlyOneBFFComponent(t *testing.T) {
	componentsDir := filepath.Join(leaflabUIDir(t), "components")

	entries, err := os.ReadDir(componentsDir)
	if err != nil {
		t.Fatalf("read %s: %v", componentsDir, err)
	}

	var matching []string
	scanned := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".templ" {
			continue
		}
		path := filepath.Join(componentsDir, e.Name())
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		scanned++
		if strings.Contains(string(b), "device_id") {
			matching = append(matching, e.Name())
		}
	}
	if scanned < 2 {
		// Guards the guard: if the data dependency silently stopped
		// resolving, the check below would vacuously pass.
		t.Fatalf("only scanned %d .templ files under %s -- check the conformance_srcs data dependency in BUILD.bazel", scanned, componentsDir)
	}

	if len(matching) != 1 {
		t.Errorf("files under leaflab/ui/components mentioning \"device_id\" = %v (%d), want exactly 1 (FR57: the device id is reachable in exactly one place)", matching, len(matching))
	}
	if len(matching) == 1 && matching[0] != "support_details.templ" {
		t.Errorf("the one component mentioning device_id is %q, want support_details.templ -- FR57's designated single home", matching[0])
	}
}
