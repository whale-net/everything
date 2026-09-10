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
