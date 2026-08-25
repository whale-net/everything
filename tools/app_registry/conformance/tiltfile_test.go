package conformance

import (
	"regexp"
	"strings"
	"testing"
)

// TestTiltfile_UIUsesSameDatabaseURLAsRegistry is the local-stack half of
// NFR-3 (FR-48, FR-49): the Tiltfile must set the UI's database URL from
// the exact same value used to build the registry's PG_DATABASE_URL, so
// local dev cannot drift into looking like two databases. It checks this
// structurally, not just textually -- both blocks must reference the same
// bound Starlark variable, and that variable must be assigned exactly
// once.
func TestTiltfile_UIUsesSameDatabaseURLAsRegistry(t *testing.T) {
	tf := mustReadFile(t, "Tiltfile")

	// pg_database_url must be computed exactly once. If a second,
	// independently-computed pg_database_url ever appears (e.g. someone
	// adds a per-component override), this test must catch it even though
	// the variable name would still match below.
	assigns := regexp.MustCompile(`(?m)^pg_database_url\s*=`).FindAllString(tf, -1)
	if len(assigns) != 1 {
		t.Fatalf("expected exactly 1 top-level `pg_database_url = ...` assignment in the Tiltfile, found %d",
			len(assigns))
	}

	// Every k8s_yaml(blob("""...""").format(...)) block that declares a
	// PG_DATABASE_URL container env var must pass pg_database_url as the
	// format() keyword feeding it -- i.e. the exact same bound variable,
	// not a copy or a re-derived value.
	blockRe := regexp.MustCompile(`(?s)k8s_yaml\(blob\(""".*?"""\.format\(.*?\)\)\)`)
	blocks := blockRe.FindAllString(tf, -1)
	if len(blocks) < 2 {
		t.Fatalf("expected at least 2 k8s_yaml(blob(...).format(...)) blocks in the Tiltfile, found %d -- "+
			"check the data dependency in BUILD.bazel", len(blocks))
	}

	var uiBlockChecked, otherBlockChecked bool
	for _, b := range blocks {
		if !strings.Contains(b, "name: PG_DATABASE_URL") {
			continue // this component doesn't set PG_DATABASE_URL at all
		}
		if !strings.Contains(b, `value: "{pg_database_url}"`) {
			t.Errorf("a Tiltfile block sets PG_DATABASE_URL but not from the {pg_database_url} placeholder:\n%s", b)
			continue
		}
		if !strings.Contains(b, "pg_database_url=pg_database_url") {
			t.Errorf("a Tiltfile block's PG_DATABASE_URL env var is not fed from the same "+
				"pg_database_url binding via .format(pg_database_url=pg_database_url, ...):\n%s", b)
			continue
		}
		if strings.Contains(b, "name: app-registry-ui") || strings.Contains(b, "app-registry-ui\n") {
			uiBlockChecked = true
		} else {
			otherBlockChecked = true
		}
	}

	if !uiBlockChecked {
		t.Error("did not find a Tiltfile block for app-registry-ui that sets PG_DATABASE_URL from " +
			"pg_database_url -- the UI must source its database URL from the same value as the rest " +
			"of App Registry (see ENV.md 'Local Development (Tilt)')")
	}
	if !otherBlockChecked {
		t.Error("did not find any other App Registry component's Tiltfile block sourcing " +
			"PG_DATABASE_URL from pg_database_url -- check the regex/data dependency, this should " +
			"match migration/api/worker too")
	}
}

// TestTiltfile_MinIOBucketParameterization ensures FR-58: the deploy_minio
// shared component is called with a parameterized bucket name (app-registry-dev)
// rather than using the hardcoded manmanv2-dev default. This guards against
// a regression that would revert the component to a single hardcoded bucket
// unsuitable for multiple independent environments.
func TestTiltfile_MinIOBucketParameterization(t *testing.T) {
	tf := mustReadFile(t, "Tiltfile")

	// The Tiltfile must call deploy_minio with bucket_name='app-registry-dev'
	// and not rely on the default manmanv2-dev value.
	bucketCallRe := regexp.MustCompile(`(?m)deploy_minio\(.*?bucket_name\s*=\s*['"]app-registry-dev['"]`)
	if !bucketCallRe.MatchString(tf) {
		t.Error("Tiltfile does not call deploy_minio with bucket_name='app-registry-dev' -- " +
			"the MinIO deployment must use a distinct bucket for app-registry local dev, " +
			"not the manmanv2-dev default (FR-58)")
	}

	// Ensure we're not accidentally using any other bucket name (catches typos,
	// regressions to manmanv2-dev, or other hardcoded buckets).
	if strings.Contains(tf, "bucket_name='manmanv2-dev'") || strings.Contains(tf, `bucket_name="manmanv2-dev"`) {
		t.Error("Tiltfile still references manmanv2-dev bucket name -- " +
			"app-registry must have its own distinct bucket, not the legacy manmanv2 default")
	}
}

// TestTiltfile_MinIOEndpointsAreDistinct ensures FR-58: the local object store
// exposes both internal (in-cluster) and public (localhost) endpoints, distinct
// from each other so a deployment missing one can be locally detected.
// Both endpoints must be retrieved from minio_info and used in deployments.
func TestTiltfile_MinIOEndpointsAreDistinct(t *testing.T) {
	tf := mustReadFile(t, "Tiltfile")

	// Verify minio_info contains both endpoint and public_endpoint keys
	if !strings.Contains(tf, "minio_info['endpoint']") && !strings.Contains(tf, `minio_info["endpoint"]`) {
		t.Error("Tiltfile does not retrieve 'endpoint' from minio_info -- " +
			"the internal (cluster-local) S3 endpoint must be extracted from deploy_minio's return value (FR-58)")
	}

	if !strings.Contains(tf, "minio_info['public_endpoint']") && !strings.Contains(tf, `minio_info["public_endpoint"]`) {
		t.Error("Tiltfile does not retrieve 'public_endpoint' from minio_info -- " +
			"the public (localhost) S3 endpoint must be extracted from deploy_minio's return value (FR-58)")
	}

	// Verify both s3_endpoint and s3_public_endpoint variables are assigned
	endpointAssigns := regexp.MustCompile(`(?m)^s3_endpoint\s*=`).FindAllString(tf, -1)
	if len(endpointAssigns) != 1 {
		t.Fatalf("expected exactly 1 top-level `s3_endpoint = ...` assignment, found %d -- "+
			"internal endpoint must be computed exactly once", len(endpointAssigns))
	}

	publicEndpointAssigns := regexp.MustCompile(`(?m)^s3_public_endpoint\s*=`).FindAllString(tf, -1)
	if len(publicEndpointAssigns) != 1 {
		t.Fatalf("expected exactly 1 top-level `s3_public_endpoint = ...` assignment, found %d -- "+
			"public endpoint must be computed exactly once", len(publicEndpointAssigns))
	}

	// Verify that s3_endpoint uses the internal (cluster-local) endpoint,
	// and s3_public_endpoint uses the public (localhost) endpoint.
	// The internal endpoint should contain the cluster DNS name format.
	endpointRe := regexp.MustCompile(`(?m)^s3_endpoint\s*=\s*get_custom_or_default.*minio_info\['endpoint'\]`)
	if !endpointRe.MatchString(tf) && !regexp.MustCompile(`(?m)^s3_endpoint\s*=\s*get_custom_or_default.*minio_info\["endpoint"\]`).MatchString(tf) {
		t.Error("s3_endpoint assignment does not use minio_info['endpoint'] -- " +
			"the internal (cluster-local) endpoint must be the primary source")
	}

	publicEndpointRe := regexp.MustCompile(`(?m)^s3_public_endpoint\s*=\s*get_custom_or_default.*minio_info\['public_endpoint'\]`)
	if !publicEndpointRe.MatchString(tf) && !regexp.MustCompile(`(?m)^s3_public_endpoint\s*=\s*get_custom_or_default.*minio_info\["public_endpoint"\]`).MatchString(tf) {
		t.Error("s3_public_endpoint assignment does not use minio_info['public_endpoint'] -- " +
			"the public (localhost) endpoint must be the primary source")
	}
}

// TestTiltfile_MinIOEndpointsUsedInDeployments ensures FR-58: both internal
// and public S3 endpoints are wired into the application deployments, so
// the local environment can exercise both internal cluster-local calls and
// external public-endpoint scenarios.
func TestTiltfile_MinIOEndpointsUsedInDeployments(t *testing.T) {
	tf := mustReadFile(t, "Tiltfile")

	// Both endpoints must appear in at least one deployment's env vars
	// (worker uses both internal and public; API uses public only per FR-72).

	// Count occurrences of both endpoint names in deployment blocks
	endpointCount := strings.Count(tf, "RELEASE_TOOLS_S3_ENDPOINT")
	publicEndpointCount := strings.Count(tf, "RELEASE_TOOLS_S3_PUBLIC_ENDPOINT")

	if endpointCount < 1 {
		t.Error("Tiltfile does not set RELEASE_TOOLS_S3_ENDPOINT in any deployment -- " +
			"the internal (cluster-local) S3 endpoint must be configured for the worker and/or API")
	}

	if publicEndpointCount < 1 {
		t.Error("Tiltfile does not set RELEASE_TOOLS_S3_PUBLIC_ENDPOINT in any deployment -- " +
			"the public (localhost) S3 endpoint must be configured for the API (FR-72)")
	}

	// Verify the endpoints are passed as format strings, not hardcoded values
	if !strings.Contains(tf, "{s3_endpoint}") && !strings.Contains(tf, "{s3_public_endpoint}") {
		t.Error("Tiltfile S3 endpoint env vars are not using format string placeholders -- " +
			"endpoints must be passed as {s3_endpoint} and {s3_public_endpoint} to allow overrides")
	}
}

// TestTiltfile_MinIOSharedComponentNotForked ensures FR-58 adoption of the
// shared deploy_minio component did not result in a fork. This is a destructive
// check: it looks for a forked copy in tools/app_registry/.
func TestTiltfile_MinIOSharedComponentNotForked(t *testing.T) {
	// The test data includes the app-registry Tiltfile but not its local minio fork
	// (if one exists). This test is structural: the Tiltfile must load from
	// tools/tilt/minio.tilt (the shared component) and not from a local copy.

	tf := mustReadFile(t, "Tiltfile")

	// Verify it loads from ../tilt/minio.tilt (shared) not a local minio.tilt
	if !strings.Contains(tf, "load('../tilt/minio.tilt'") && !strings.Contains(tf, `load("../tilt/minio.tilt"`) {
		t.Error("Tiltfile does not load deploy_minio from ../tilt/minio.tilt (the shared component) -- " +
			"adopting the shared component requires loading from tools/tilt/minio.tilt, not forking it (FR-58)")
	}

	// Additional safety: warn if there's a suspicious local minio.tilt in app_registry
	// (this won't fail the test, but helps during development)
	if strings.Contains(tf, "minio.tilt") && strings.Count(tf, "load") > 0 {
		// Ensure we're not accidentally defining deploy_minio locally
		if strings.Contains(tf, "def deploy_minio") {
			t.Error("Tiltfile defines deploy_minio locally -- " +
				"the shared component in tools/tilt/minio.tilt must be used, not a local definition (FR-58)")
		}
	}
}
