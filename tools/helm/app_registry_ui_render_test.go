package helm

import (
	"strings"
	"testing"
)

// This file is the App Registry UI half of #656's deployment-rendering
// checks (FR-47, FR-57): it exercises the exact secretEnv shape documented
// in tools/app_registry/ENV.md's "UI (`app-registry-ui`)" section --
// SECRET_KEY, OIDC_CLIENT_SECRET, and the database URL (PG_DATABASE_URL)
// sourced via secretKeyRef -- rather than a generic fixture, so a
// regression in either the template or that doc's example drifting apart
// is caught directly.

// fixtureAppRegistryUI mirrors tools/app_registry/ui:ui_metadata's shape
// (external-api, port 8000) plus the secretEnv block from ENV.md's
// "Deploying for real" example, byte-for-byte.
func fixtureAppRegistryUI() ValuesData {
	return ValuesData{
		Global:          GlobalConfig{Namespace: "app-registry", Environment: "prod"},
		IngressDefaults: IngressDefaultsConfig{Enabled: false},
		OTLP:            OTLPConfig{Endpoint: "http://otel-collector:4317"},
		Apps: map[string]AppConfig{
			"app-registry-ui": {
				Type:     "external-api",
				Domain:   "app-registry",
				Image:    "ghcr.io/whale-net/app-registry-ui",
				ImageTag: "v1.0.0",
				Port:     8000,
				Replicas: 1,
				Resources: ValuesResourceConfig{
					Requests: ResourceValues{CPU: "50m", Memory: "128Mi"},
					Limits:   ResourceValues{CPU: "100m", Memory: "256Mi"},
				},
				// Non-secret env, matching ENV.md's UI table -- must
				// coexist with secretEnv below without leaking it.
				Env: map[string]string{
					"AUTH_MODE":        "oidc",
					"GRPC_AUTH_MODE":   "oidc",
					"REGISTRY_API_URL": "app-registry-api:50051",
				},
				SecretEnv: []SecretEnvEntry{
					{Name: "SECRET_KEY", SecretName: "app-registry-ui-secrets", Key: "secret-key"},
					{Name: "OIDC_CLIENT_SECRET", SecretName: "app-registry-ui-secrets", Key: "oidc-client-secret"},
					{Name: "PG_DATABASE_URL", SecretName: "app-registry-ui-secrets", Key: "database-url"},
				},
			},
		},
	}
}

// appRegistryUISecretVars is the set this test guards -- kept as a slice
// (not re-derived from the fixture) so the assertions below don't
// tautologically pass if the fixture itself is edited incorrectly.
var appRegistryUISecretVars = []string{"SECRET_KEY", "OIDC_CLIENT_SECRET", "PG_DATABASE_URL"}

// TestRenderDeployment_AppRegistryUI_SecretsAsReferences is the FR-57/FR-47
// secret-delivery check: SECRET_KEY, OIDC_CLIENT_SECRET, and the database
// URL must render as secretKeyRef references, and grepping the rendered
// manifest for their literal values must find nothing.
func TestRenderDeployment_AppRegistryUI_SecretsAsReferences(t *testing.T) {
	data := fixtureAppRegistryUI()
	values := valuesMapFromData(t, data)
	rendered := renderTemplateFile(t, deploymentTemplatePath, defaultHelmRoot(values))
	container := decodeSingleContainer(t, rendered)

	app := data.Apps["app-registry-ui"]
	wantKey := map[string]SecretEnvEntry{}
	for _, se := range app.SecretEnv {
		wantKey[se.Name] = se
	}

	seen := map[string]bool{}
	for _, ev := range container.Env {
		want, ok := wantKey[ev.Name]
		if !ok {
			continue
		}
		seen[ev.Name] = true
		if ev.Value != "" {
			t.Errorf("%s: rendered with a literal value %q -- must be secretKeyRef-only", ev.Name, ev.Value)
		}
		if ev.ValueFrom == nil || ev.ValueFrom.SecretKeyRef == nil {
			t.Errorf("%s: rendered without valueFrom.secretKeyRef", ev.Name)
			continue
		}
		if ev.ValueFrom.SecretKeyRef.Name != want.SecretName || ev.ValueFrom.SecretKeyRef.Key != want.Key {
			t.Errorf("%s: secretKeyRef = {name: %q, key: %q}, want {name: %q, key: %q}",
				ev.Name, ev.ValueFrom.SecretKeyRef.Name, ev.ValueFrom.SecretKeyRef.Key, want.SecretName, want.Key)
		}
	}
	for _, name := range appRegistryUISecretVars {
		if !seen[name] {
			t.Errorf("%s: not found in rendered env at all", name)
		}
	}

	// Grep the full rendered manifest text -- not just this var's own env
	// entry -- for a literal `value:` line naming any of these vars, and
	// for the UI's own well-known dev-mode default (ui/main.go's
	// SECRET_KEY fallback), which must never leak into a real deployment.
	for _, line := range strings.Split(rendered, "\n") {
		for _, name := range appRegistryUISecretVars {
			if strings.Contains(line, name) && strings.Contains(line, "value:") {
				t.Errorf("literal value alongside secret-sourced env var %s: %q", name, line)
			}
		}
	}
	if strings.Contains(rendered, "dev-secret-key-change-in-production") {
		t.Error("rendered manifest contains the UI's dev-mode SECRET_KEY default -- " +
			"must never appear in a real deployment's rendered chart")
	}
}

// TestRenderDeployment_AppRegistryUI_NoSecretEnv_Deterministic is the
// no-secret-regression case (complementing
// TestRenderDeployment_ByteIdenticalToPreChangeTemplate's generic golden):
// an app-registry-ui-shaped app declaring no secret-sourced env renders
// deterministically and emits no secretKeyRef/envFrom machinery at all.
func TestRenderDeployment_AppRegistryUI_NoSecretEnv_Deterministic(t *testing.T) {
	data := fixtureAppRegistryUI()
	app := data.Apps["app-registry-ui"]
	app.SecretEnv = nil
	data.Apps["app-registry-ui"] = app

	values := valuesMapFromData(t, data)
	root := defaultHelmRoot(values)

	first := renderTemplateFile(t, deploymentTemplatePath, root)
	second := renderTemplateFile(t, deploymentTemplatePath, root)
	if first != second {
		t.Fatalf("rendering the same no-secretEnv values twice produced different output\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if strings.Contains(first, "secretKeyRef") {
		t.Error("no secretEnv declared, but rendered manifest contains secretKeyRef")
	}
	if strings.Contains(first, "envFrom") {
		t.Error("no envFrom declared, but rendered manifest contains envFrom")
	}
}
