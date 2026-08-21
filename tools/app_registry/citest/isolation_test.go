package citest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// workflowIsolation is the subset of a workflow file's top-level shape this
// test cares about: which concurrency group it claims, and which events it
// triggers on. `on:` can be a bare string, a list of strings, or a map of
// event name -> config, so it's decoded as a yaml.Node and walked by hand
// rather than into a fixed Go type.
type workflowIsolation struct {
	Concurrency struct {
		Group string `yaml:"group"`
	} `yaml:"concurrency"`
	Triggers []string
}

// decodeWorkflowIsolation reads path (repo-relative to dir) and extracts its
// concurrency group and trigger event names.
func decodeWorkflowIsolation(t *testing.T, dir, path string) workflowIsolation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(root.Content) == 0 {
		t.Fatalf("%s: empty document", path)
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		t.Fatalf("%s: top-level node is not a mapping", path)
	}

	var out workflowIsolation
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i]
		val := doc.Content[i+1]
		switch key.Value {
		case "concurrency":
			var c struct {
				Group string `yaml:"group"`
			}
			if err := val.Decode(&c); err != nil {
				t.Fatalf("%s: decode concurrency: %v", path, err)
			}
			out.Concurrency.Group = c.Group
		case "on":
			out.Triggers = triggerNames(t, path, val)
		}
	}
	return out
}

// triggerNames returns the event names a workflow's `on:` node declares,
// regardless of whether it's a scalar, a sequence, or a mapping.
func triggerNames(t *testing.T, path string, on *yaml.Node) []string {
	t.Helper()
	switch on.Kind {
	case yaml.ScalarNode:
		return []string{on.Value}
	case yaml.SequenceNode:
		var out []string
		for _, n := range on.Content {
			out = append(out, n.Value)
		}
		return out
	case yaml.MappingNode:
		var out []string
		for i := 0; i+1 < len(on.Content); i += 2 {
			out = append(out, on.Content[i].Value)
		}
		return out
	default:
		t.Fatalf("%s: unrecognized `on:` node kind %v", path, on.Kind)
		return nil
	}
}

// dispatchScopedTriggers are GitHub event types that are inherently scoped
// to the single workflow file a caller names, not broadcast to every
// workflow that declares them: dispatching release-v2.yml's
// workflow_dispatch can never also start release.yml, no matter how many
// other workflow files also declare a `workflow_dispatch:` trigger. FR3's
// "no trigger release.yml itself responds to" concern is about events that
// *would* fire both workflows for the same commit (push, schedule,
// pull_request, ...) -- it isn't violated by both files opting into the
// same dispatch-style event name, which is why both are allowed to (and
// currently do) declare `workflow_dispatch`. What actually keeps
// release-v2.yml from being a second human entry point for the same
// release is the bot-identity gate below, not trigger-name disjointness.
var dispatchScopedTriggers = map[string]bool{
	"workflow_dispatch":   true,
	"repository_dispatch": true,
}

// requiresBotIdentity does a light textual check that path's job(s) reject
// non-bot triggering_actor values, i.e. that a dispatch-scoped trigger is
// gated to machine callers rather than being a human entry point. This is
// deliberately not a full workflow interpreter -- it just guards against
// the gate being deleted outright.
func requiresBotIdentity(t *testing.T, dir, path string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Contains(string(data), "github.triggering_actor") &&
		strings.Contains(string(data), `[bot]`)
}

// TestReleaseV2IsolatedFromReleaseV1 is the automated check for FR2/FR3/C2:
// release-v2.yml must never cancel/block a release.yml run for the same
// commit (distinct concurrency.group), and must never fire on a broadcast
// event release.yml itself responds to (disjoint trigger sets, excluding
// dispatch-scoped events -- see dispatchScopedTriggers). Both properties
// are enforced by review today; this test makes a regression in either one
// fail CI instead of waiting for a human to notice two releases colliding.
func TestReleaseV2IsolatedFromReleaseV1(t *testing.T) {
	dir := githubDir(t)

	v1Path := filepath.Join("workflows", "release.yml")
	v2Path := filepath.Join("workflows", "release-v2.yml")
	v1 := decodeWorkflowIsolation(t, dir, v1Path)
	v2 := decodeWorkflowIsolation(t, dir, v2Path)

	if v1.Concurrency.Group == "" {
		t.Fatalf("release.yml: no concurrency.group found -- did the file move or get restructured?")
	}
	if v2.Concurrency.Group == "" {
		t.Fatalf("release-v2.yml: no concurrency.group found -- did the file move or get restructured?")
	}
	if v1.Concurrency.Group == v2.Concurrency.Group {
		t.Errorf("release.yml and release-v2.yml share concurrency.group %q -- a v1 run and a v2 run for the same commit would cancel/block each other (FR2, C2)", v1.Concurrency.Group)
	}

	if len(v1.Triggers) == 0 {
		t.Fatalf("release.yml: no triggers found under `on:` -- did the file move or get restructured?")
	}
	if len(v2.Triggers) == 0 {
		t.Fatalf("release-v2.yml: no triggers found under `on:` -- did the file move or get restructured?")
	}

	v1Set := map[string]bool{}
	for _, e := range v1.Triggers {
		v1Set[e] = true
	}
	for _, e := range v2.Triggers {
		if dispatchScopedTriggers[e] {
			continue
		}
		if v1Set[e] {
			t.Errorf("release-v2.yml declares broadcast trigger %q, which release.yml also responds to -- both would fire for the same event (FR3)", e)
		}
	}

	// For any dispatch-scoped trigger both files share, the substitute
	// isolation guarantee is that neither is a human entry point.
	for e := range dispatchScopedTriggers {
		if !v1Set[e] {
			continue
		}
		v2Has := false
		for _, t2 := range v2.Triggers {
			if t2 == e {
				v2Has = true
				break
			}
		}
		if !v2Has {
			continue
		}
		if !requiresBotIdentity(t, dir, v1Path) {
			t.Errorf("release.yml declares %q but has no bot-identity gate on github.triggering_actor -- it would be a human entry point overlapping release-v2.yml's", e)
		}
		if !requiresBotIdentity(t, dir, v2Path) {
			t.Errorf("release-v2.yml declares %q but has no bot-identity gate on github.triggering_actor -- it would be a second human entry point alongside release.yml's", e)
		}
	}
}
