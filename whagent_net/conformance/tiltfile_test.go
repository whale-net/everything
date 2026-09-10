// Package conformance holds structural conformance tests for whagent-net's
// Tiltfile (mirrors tools/app_registry/conformance's pattern). These tests
// read the real checked-in Tiltfile as data (never a copy or a fixture) so
// a regression in the local-dev wiring is caught directly, not by proxy.
package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// whagentNetDir locates whagent_net, using BUILD.bazel (staged as a data
// dependency via the "build_file" filegroup) as the marker file. Bazel
// stages data relative to the runfiles root; a plain `go test` run from
// this package's own directory finds it by walking up.
func whagentNetDir(t *testing.T) string {
	t.Helper()
	for _, c := range []string{".", "..", "../..", "../../..", "../../../.."} {
		marker := filepath.Join(c, "whagent_net", "BUILD.bazel")
		if st, err := os.Stat(marker); err == nil && !st.IsDir() {
			return filepath.Join(c, "whagent_net")
		}
	}
	t.Fatal("could not locate whagent_net/BUILD.bazel -- check the data dependency in BUILD.bazel")
	return ""
}

// mustReadFile reads a file relative to whagentNetDir, failing the test on
// any error.
func mustReadFile(t *testing.T, rel string) string {
	t.Helper()
	dir := whagentNetDir(t)
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v (check the data dependency in BUILD.bazel)", rel, err)
	}
	return string(b)
}

// TestTiltfile_APIWorkerUIShareRabbitMQURL is a regression test for issue
// #2341 (FR5 StreamEvents): whagent-net-api's deployment block must set
// RABBITMQ_URL from the same top-level rabbitmq_url binding that
// whagent-net-worker and whagent-net-ui already use. Without it, api's
// initializeEventsConsumer degrades non-fatally and StreamEvents always
// reports Unavailable in the default dev environment (validation finding
// #2338, part (a)).
func TestTiltfile_APIWorkerUIShareRabbitMQURL(t *testing.T) {
	tf := mustReadFile(t, "Tiltfile")

	// rabbitmq_url must be computed exactly once. If a second,
	// independently-computed rabbitmq_url ever appears, this test must
	// catch it even though the variable name would still match below.
	assigns := regexp.MustCompile(`(?m)^rabbitmq_url\s*=`).FindAllString(tf, -1)
	if len(assigns) != 1 {
		t.Fatalf("expected exactly 1 top-level `rabbitmq_url = ...` assignment in the Tiltfile, found %d",
			len(assigns))
	}

	// Every k8s_yaml(blob("""...""").format(...)) block that declares a
	// RABBITMQ_URL container env var must pass rabbitmq_url as the
	// format() keyword feeding it -- i.e. the exact same bound variable,
	// not a copy or a re-derived value.
	blockRe := regexp.MustCompile(`(?s)k8s_yaml\(blob\(""".*?"""\.format\(.*?\)\)\)`)
	blocks := blockRe.FindAllString(tf, -1)
	if len(blocks) < 3 {
		t.Fatalf("expected at least 3 k8s_yaml(blob(...).format(...)) blocks in the Tiltfile, found %d -- "+
			"check the data dependency in BUILD.bazel", len(blocks))
	}

	checked := map[string]bool{}
	for _, b := range blocks {
		if !strings.Contains(b, "name: RABBITMQ_URL") {
			continue // this component doesn't set RABBITMQ_URL at all
		}
		if !strings.Contains(b, `value: "{rabbitmq_url}"`) {
			t.Errorf("a Tiltfile block sets RABBITMQ_URL but not from the {rabbitmq_url} placeholder:\n%s", b)
			continue
		}
		if !strings.Contains(b, "rabbitmq_url=rabbitmq_url") {
			t.Errorf("a Tiltfile block's RABBITMQ_URL env var is not fed from the same "+
				"rabbitmq_url binding via .format(rabbitmq_url=rabbitmq_url, ...):\n%s", b)
			continue
		}
		for _, name := range []string{"whagent-net-api", "whagent-net-worker", "whagent-net-ui"} {
			if strings.Contains(b, "name: "+name+"\n") || strings.Contains(b, "app: "+name+"\n") {
				checked[name] = true
			}
		}
	}

	for _, name := range []string{"whagent-net-api", "whagent-net-worker", "whagent-net-ui"} {
		if !checked[name] {
			t.Errorf("did not find a Tiltfile block for %s that sets RABBITMQ_URL from rabbitmq_url -- "+
				"api/worker/ui must all source their RabbitMQ URL from the same value (issue #2341)", name)
		}
	}
}

// TestTiltfile_MCPOAuthEnvGatedBehindFlag is a regression test for issue
// #2342 (FR9): whagent-net-mcp's PG_DATABASE_URL and the three
// WHAGENT_MCP_KEYCLOAK_* vars must only be wired when
// ENABLE_WHAGENT_NET_MCP_OAUTH is truthy, never unconditionally in the
// static deployment blob. main.go's initializeTokenExchange treats "DB
// reachable + mcp_credential table present + token-exchange config
// incomplete" as a fatal NFR8 fail-loud error by design -- setting
// PG_DATABASE_URL unconditionally while leaving the Keycloak vars unset
// (the prior Tiltfile default) crash-loops every fresh mcp pod once the
// 004_mcpauth_credential migration is applied (validation finding #2338,
// part (b)).
func TestTiltfile_MCPOAuthEnvGatedBehindFlag(t *testing.T) {
	tf := mustReadFile(t, "Tiltfile")

	// The gate must be evaluated exactly once, defaulting to disabled --
	// if this default ever flips to 'true' (or the gate is removed), mcp
	// goes back to crash-looping by default.
	gateRe := regexp.MustCompile(
		`(?m)^if get_env_bool\('ENABLE_WHAGENT_NET_MCP_OAUTH', default='false'\):$`)
	gates := gateRe.FindAllString(tf, -1)
	if len(gates) != 1 {
		t.Fatalf("expected exactly 1 top-level `if get_env_bool('ENABLE_WHAGENT_NET_MCP_OAUTH', "+
			"default='false'):` guard in the Tiltfile, found %d", len(gates))
	}

	// Everything the gate populates (mcp_oauth_env_lines) must appear
	// between the gate and the next top-level statement -- if one of the
	// gated vars leaks outside the gated block, this capture won't
	// contain it and the presence check below will catch it.
	gateBlockRe := regexp.MustCompile(
		`(?ms)^if get_env_bool\('ENABLE_WHAGENT_NET_MCP_OAUTH', default='false'\):\n(.*?)\n(?:^\S|\z)`)
	m := gateBlockRe.FindStringSubmatch(tf)
	if m == nil {
		t.Fatal("could not extract the ENABLE_WHAGENT_NET_MCP_OAUTH gate's body -- check the regex " +
			"against the Tiltfile's current structure")
	}
	gatedBody := m[1]

	gatedNames := []string{
		"PG_DATABASE_URL",
		"WHAGENT_MCP_KEYCLOAK_CLIENT_ID",
		"WHAGENT_MCP_KEYCLOAK_CLIENT_SECRET",
		"WHAGENT_MCP_KEYCLOAK_TOKEN_URL",
	}
	for _, name := range gatedNames {
		wantLine := "name: " + name
		if !strings.Contains(gatedBody, wantLine) {
			t.Errorf("ENABLE_WHAGENT_NET_MCP_OAUTH gate body does not set %q -- "+
				"expected a `- name: %s` line inside the gated block", name, name)
		}
	}

	// The mcp deployment's k8s_yaml(blob(...).format(...)) block must
	// consume the gate's output via the {mcp_oauth_env} placeholder, fed
	// from the same mcp_oauth_env binding -- not a hardcoded value. Note
	// this block is matched from the raw, unevaluated Tiltfile source, so
	// {mcp_oauth_env} still appears as a literal placeholder here (unlike
	// PG_DATABASE_URL, this name is unique to mcp: api/worker/ui set it
	// too, so a whole-file occurrence count would false-positive on their
	// unrelated, legitimate uses).
	blockRe := regexp.MustCompile(`(?s)k8s_yaml\(blob\(""".*?"""\.format\(.*?\)\)\)`)
	blocks := blockRe.FindAllString(tf, -1)
	var mcpBlock string
	for _, b := range blocks {
		if strings.Contains(b, "app: whagent-net-mcp\n") {
			mcpBlock = b
			break
		}
	}
	if mcpBlock == "" {
		t.Fatal("did not find whagent-net-mcp's k8s_yaml(blob(...).format(...)) deployment block")
	}
	for _, name := range gatedNames {
		wantLine := "name: " + name
		if strings.Contains(mcpBlock, wantLine) {
			t.Errorf("whagent-net-mcp's static deployment blob hardcodes `- name: %s` directly -- "+
				"it must only be set via the ENABLE_WHAGENT_NET_MCP_OAUTH gate's {mcp_oauth_env} "+
				"placeholder, never unconditionally (that combination fatally crash-loops mcp per "+
				"NFR8, issue #2342)", name)
		}
	}
	if !strings.Contains(mcpBlock, "{mcp_oauth_env}") {
		t.Error("whagent-net-mcp's deployment block does not reference the {mcp_oauth_env} placeholder -- " +
			"PG_DATABASE_URL/Keycloak vars must be injected via the opt-in gate, not hardcoded")
	}
	if !strings.Contains(mcpBlock, "mcp_oauth_env=mcp_oauth_env") {
		t.Error("whagent-net-mcp's deployment block's .format(...) call does not pass " +
			"mcp_oauth_env=mcp_oauth_env -- the {mcp_oauth_env} placeholder must be fed from the gate's output")
	}
}
