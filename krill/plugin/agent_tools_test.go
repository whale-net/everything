// Package plugin_test guards the contract between a plugin's agent
// frontmatter and the MCP servers its own manifest actually registers.
//
// An agent's `tools:` list is an allowlist: it can only call tools whose
// names match it. A plugin's MCP tool names are prefixed
// `mcp__plugin_<plugin>_<server>__`, so a frontmatter entry naming a
// server that the plugin's .mcp.json does not register silently matches
// zero tools -- the persona keeps its `tools:` line looking complete while
// every tool it actually needs is unreachable.
//
// That is not hypothetical: krill-work's task-lifecycle tools were moved
// from /mcp/design to a dedicated /mcp/work mount, and the manifest servers
// renamed krill-mcp-design-* -> krill-mcp-work-*, while the
// krill/plugin/work/agents/*.md frontmatter lines still named the old
// servers. The affected personas (worker, validator, mergepush, planner,
// system-validator) lost claim_task, complete_task, heartbeat_task,
// get_task, list_tasks, record_note, and the rest, with nothing failing.
//
// These tests read the real committed files, so a future rename either
// updates the frontmatter in the same change or fails here.
package plugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolPrefixRe matches a single `mcp__plugin_<plugin>_<server>__*` entry.
// Both groups are non-greedy because the plugin name itself contains
// hyphens, so `krill-mcp-work-prod` is only split correctly if the
// `<plugin>` group stops at the first `_`.
var toolPrefixRe = regexp.MustCompile(`mcp__plugin_([a-z0-9-]+?)_([a-z0-9-]+?)__`)

// manifest is the subset of .mcp.json / mcp_config.json this test reads.
type manifest struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

// pluginRoot resolves krill/plugin/ through Bazel runfiles, so the test
// reads the same committed files the plugins are loaded from. The
// _main/ prefix is the workspace name rules_go stages sources under.
//
// Anchors on design/.mcp.json and stats it: runfiles.Rlocation only builds
// a path, it does not confirm the file is a declared data dep, so without
// the Stat a missing dep surfaces as a confusing "no such file" further
// down instead of naming the BUILD file that dropped it.
func pluginRoot(t *testing.T) string {
	t.Helper()

	anchor := "_main/krill/plugin/design/.mcp.json"
	path, err := runfiles.Rlocation(anchor)
	require.NoErrorf(t, err, "runfiles.Rlocation(%s): %v", anchor, err)

	_, err = os.Stat(path)
	require.NoErrorf(t, err,
		"%s is not in the runfiles tree -- is :plugin_files still a data dep of this test target?", anchor)

	return filepath.Dir(filepath.Dir(path))
}

// agentTools returns the comma-separated `tools:` frontmatter value for
// each agent markdown file in dir, keyed by filename.
func agentTools(t *testing.T, dir string) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	tools := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		if line, ok := frontmatterField(string(raw), "tools"); ok {
			tools[e.Name()] = line
		}
	}
	return tools
}

// frontmatterField returns the value of a top-level YAML key in the file's
// leading `---` frontmatter block. Hand-rolled rather than pulled from a
// YAML library: these files are a flat key/value block, and a dependency
// would be heavier than the parse.
func frontmatterField(doc, key string) (string, bool) {
	if !strings.HasPrefix(doc, "---\n") {
		return "", false
	}
	for _, line := range strings.Split(doc, "\n")[1:] {
		if line == "---" {
			break
		}
		if name, value, ok := strings.Cut(line, ": "); ok && name == key {
			return value, true
		}
	}
	return "", false
}

// serverNames reads the registered server names from a plugin manifest.
// Both .mcp.json and mcp_config.json use the same mcpServers key, so one
// reader covers each.
func serverNames(t *testing.T, path string) map[string]bool {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var m manifest
	require.NoError(t, json.Unmarshal(raw, &m))

	names := map[string]bool{}
	for name := range m.MCPServers {
		names[name] = true
	}
	return names
}

// toolRefs returns the (plugin, server) pair for every mcp__plugin_ entry
// in one `tools:` frontmatter value.
func toolRefs(line string) [][2]string {
	var out [][2]string
	for _, part := range strings.Split(line, ",") {
		if m := toolPrefixRe.FindStringSubmatch(strings.TrimSpace(part)); m != nil {
			out = append(out, [2]string{m[1], m[2]})
		}
	}
	return out
}

// TestAgentToolsMatchRegisteredServers is the guard itself: every
// `mcp__plugin_<plugin>_<server>__*` entry in an agent's `tools:`
// frontmatter must name a server that the *same* plugin's manifest
// registers.
//
// Plugin-name equality is the load-bearing part. A krill-work agent asking
// for `krill-mcp-design-prod` resolves against the krill-work plugin's own
// server table, where that name does not exist -- the design plugin having
// it is irrelevant, since the prefix is scoped to one plugin.
func TestAgentToolsMatchRegisteredServers(t *testing.T) {
	t.Parallel()

	root := pluginRoot(t)
	plugins := []struct {
		name string
		dir  string
	}{
		{"krill-work", "work"},
		{"krill-design", "design"},
	}

	for _, p := range plugins {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()

			registered := serverNames(t, filepath.Join(root, p.dir, ".mcp.json"))
			require.NotEmpty(t, registered, "%s registers no MCP servers", p.dir)

			agents := agentTools(t, filepath.Join(root, p.dir, "agents"))
			require.NotEmpty(t, agents, "no agents with tools: frontmatter in %s", p.dir)

			for file, line := range agents {
				for _, ref := range toolRefs(line) {
					plugin, server := ref[0], ref[1]
					assert.Equalf(t, p.name, plugin,
						"%s/agents/%s references another plugin's tools (%s_*)",
						p.dir, file, plugin)
					assert.Truef(t, registered[server],
						"%s/agents/%s asks for %s_*, which %s does not register "+
							"(registered: %s) -- the persona can call none of it",
						p.dir, file, server, p.dir, sortedKeys(registered))
				}
			}
		})
	}
}

// TestAgentManifestsAgree pins the two manifest files per plugin to the same
// server set. They are near-duplicates consumed by different front doors
// (`.mcp.json` by Claude Code, `mcp_config.json` by another consumer), and
// a server added to one but not the other fails the same way this test's
// sibling does: the persona's tools resolve against a table that is missing
// a mount the tool lives on.
func TestAgentManifestsAgree(t *testing.T) {
	t.Parallel()

	root := pluginRoot(t)
	for _, dir := range []string{"work", "design"} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			claude := serverNames(t, filepath.Join(root, dir, ".mcp.json"))
			other := serverNames(t, filepath.Join(root, dir, "mcp_config.json"))
			assert.Equal(t, sortedKeys(claude), sortedKeys(other),
				"krill/plugin/%s/.mcp.json and mcp_config.json register different servers", dir)
		})
	}
}

// TestWorkAgentsReachTaskLifecycleTools states the split-mount invariant
// in tool terms rather than server-name terms: every persona whose
// instructions tell it to run a work-axis task-lifecycle tool must be able
// to reach the /mcp/work mount that tool lives on.
//
// Server-name equality alone is too weak a guard -- a persona could name
// krill-mcp-work-prod and still be missing the tool if someone moves the
// tool off that mount. This test pins the mount, not just the spelling.
func TestWorkAgentsReachTaskLifecycleTools(t *testing.T) {
	t.Parallel()

	// Personas that call at least one tool registered on /mcp/work.
	// help.md and quick-task.md are absent deliberately: help.md is the
	// shared triage persona (symlinked into both plugins, no MCP writes),
	// and quick-task.md only ever reads spec slices.
	needWorkMount := []string{
		"worker.md",
		"validator.md",
		"mergepush.md",
		"planner.md",
		"system-validator.md",
	}

	tools := agentTools(t, filepath.Join(pluginRoot(t), "work", "agents"))
	require.NotEmpty(t, tools)

	for _, file := range needWorkMount {
		t.Run(file, func(t *testing.T) {
			line, ok := tools[file]
			require.True(t, ok, "%s has no tools: frontmatter", file)

			var found bool
			for _, ref := range toolRefs(line) {
				if ref[0] == "krill-work" && strings.HasPrefix(ref[1], "krill-mcp-work-") {
					found = true
				}
			}
			assert.True(t, found,
				"%s runs task-lifecycle tools (claim_task/complete_task/get_task/...) "+
					"but its allowlist names no /mcp/work server; it has: %s", file, line)
		})
	}
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
