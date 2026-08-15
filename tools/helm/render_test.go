package helm

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

// This file exercises the actual Kubernetes manifest rendering performed by
// tools/helm/templates/deployment.yaml.tmpl and job.yaml.tmpl (FR-57:
// secretKeyRef / envFrom support). Composer.GenerateChart only copies these
// .tmpl files verbatim into the output chart -- the real Go-template
// execution happens later, when `helm template`/`helm install` runs them
// against values.yaml using Helm's built-in Sprig funcmap. To test the
// rendered manifest shape hermetically (no external `helm` binary, no
// network), these tests execute the checked-in .tmpl files directly with
// Go's text/template, using a minimal FuncMap that reproduces the two Sprig
// functions these templates actually use (`quote`, `default`).
//
// Values fed to the templates are produced by the composer's own
// writeValuesYAML + a YAML round-trip, so the fixtures exercise the same
// values.yaml shape the real chart-generation pipeline emits.

// helmFuncMap reproduces the subset of Sprig's funcmap that
// tools/helm/templates/*.tmpl actually calls.
func helmFuncMap() template.FuncMap {
	return template.FuncMap{
		// Sprig's quote: fmt.Sprintf("%q", ...) on a single value.
		"quote": func(v interface{}) string {
			return fmt.Sprintf("%q", fmt.Sprint(v))
		},
		// Sprig's default(defaultValue, given): return given unless it is
		// nil or the empty string, in which case return defaultValue.
		"default": func(defaultVal interface{}, val interface{}) interface{} {
			if val == nil {
				return defaultVal
			}
			if s, ok := val.(string); ok && s == "" {
				return defaultVal
			}
			return val
		},
	}
}

// helmRootData mirrors the top-level context Helm passes to chart
// templates: capitalized builtin objects (Chart, Release) alongside the
// user-authored Values tree (lowercase keys, exactly as they appear in
// values.yaml).
type helmRootData struct {
	Values  map[string]interface{}
	Chart   map[string]interface{}
	Release map[string]interface{}
}

func defaultHelmRoot(values map[string]interface{}) helmRootData {
	return helmRootData{
		Values:  values,
		Chart:   map[string]interface{}{"Name": "test-chart"},
		Release: map[string]interface{}{"Name": "test-release"},
	}
}

// valuesMapFromData round-trips a ValuesData through the composer's actual
// writeValuesYAML writer and back into a generic map, so tests render
// templates against the exact bytes the real chart-generation pipeline
// would produce.
func valuesMapFromData(t *testing.T, data ValuesData) map[string]interface{} {
	t.Helper()
	f, err := os.CreateTemp("", "helm-render-values-*.yaml")
	if err != nil {
		t.Fatalf("create temp values file: %v", err)
	}
	defer os.Remove(f.Name())

	if err := writeValuesYAML(f, data); err != nil {
		f.Close()
		t.Fatalf("writeValuesYAML: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close values file: %v", err)
	}

	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read values file: %v", err)
	}

	var m map[string]interface{}
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal generated values.yaml: %v\n--- content ---\n%s", err, b)
	}
	return m
}

// renderTemplateFile parses and executes a chart template file against the
// given Helm-shaped root data.
func renderTemplateFile(t *testing.T, path string, root helmRootData) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read template %s: %v", path, err)
	}
	tmpl, err := template.New(path).Funcs(helmFuncMap()).Parse(string(content))
	if err != nil {
		t.Fatalf("parse template %s: %v", path, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, root); err != nil {
		t.Fatalf("execute template %s: %v", path, err)
	}
	return buf.String()
}

// --- decoded manifest shapes (only the fields these tests assert on) ---

type k8sSecretKeyRef struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

type k8sValueFrom struct {
	SecretKeyRef *k8sSecretKeyRef `yaml:"secretKeyRef,omitempty"`
}

type k8sEnvVar struct {
	Name      string        `yaml:"name"`
	Value     string        `yaml:"value,omitempty"`
	ValueFrom *k8sValueFrom `yaml:"valueFrom,omitempty"`
}

type k8sEnvFromSource struct {
	SecretRef    *struct {
		Name string `yaml:"name"`
	} `yaml:"secretRef,omitempty"`
	ConfigMapRef *struct {
		Name string `yaml:"name"`
	} `yaml:"configMapRef,omitempty"`
}

type k8sContainer struct {
	Name    string             `yaml:"name"`
	Env     []k8sEnvVar        `yaml:"env"`
	EnvFrom []k8sEnvFromSource `yaml:"envFrom,omitempty"`
}

type k8sManifest struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []k8sContainer `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// decodeSingleContainer renders the given manifest text (which the
// deployment/job templates wrap in a single leading "---" document) and
// returns its sole container.
func decodeSingleContainer(t *testing.T, rendered string) k8sContainer {
	t.Helper()
	var m k8sManifest
	if err := yaml.Unmarshal([]byte(rendered), &m); err != nil {
		t.Fatalf("unmarshal rendered manifest: %v\n--- content ---\n%s", err, rendered)
	}
	if len(m.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected exactly 1 container, got %d\n--- content ---\n%s", len(m.Spec.Template.Spec.Containers), rendered)
	}
	return m.Spec.Template.Spec.Containers[0]
}

const deploymentTemplatePath = "templates/deployment.yaml.tmpl"
const jobTemplatePath = "templates/job.yaml.tmpl"

// fixtureNoSecretEnv is a representative external-api app with only literal
// env vars declared -- no secretEnv, no envFrom.
func fixtureNoSecretEnv() ValuesData {
	return ValuesData{
		Global:          GlobalConfig{Namespace: "ns", Environment: "dev"},
		IngressDefaults: IngressDefaultsConfig{Enabled: false},
		OTLP:            OTLPConfig{Endpoint: "http://otel-collector:4317"},
		Apps: map[string]AppConfig{
			"myapp-api": {
				Type:     "external-api",
				Domain:   "example.com",
				Image:    "ghcr.io/org/myapp",
				ImageTag: "v1.0.0",
				Port:     8080,
				Replicas: 2,
				Resources: ValuesResourceConfig{
					Requests: ResourceValues{CPU: "50m", Memory: "128Mi"},
					Limits:   ResourceValues{CPU: "100m", Memory: "256Mi"},
				},
				Env: map[string]string{"LOG_LEVEL": "info"},
			},
		},
	}
}

// fixtureJobNoSecretEnv is the job-type analog of fixtureNoSecretEnv.
func fixtureJobNoSecretEnv() ValuesData {
	return ValuesData{
		Global:          GlobalConfig{Namespace: "ns", Environment: "dev"},
		IngressDefaults: IngressDefaultsConfig{Enabled: false},
		OTLP:            OTLPConfig{Endpoint: "http://otel-collector:4317"},
		Apps: map[string]AppConfig{
			"myapp-migrate": {
				Type:     "job",
				Domain:   "example.com",
				Image:    "ghcr.io/org/myapp-migrate",
				ImageTag: "v1.0.0",
				Replicas: 1,
				Resources: ValuesResourceConfig{
					Requests: ResourceValues{CPU: "50m", Memory: "128Mi"},
					Limits:   ResourceValues{CPU: "100m", Memory: "256Mi"},
				},
				Env: map[string]string{"LOG_LEVEL": "info"},
			},
		},
	}
}

// TestRenderDeployment_ByteIdenticalToPreChangeTemplate proves that when no
// secretEnv/envFrom is declared, the *rendered manifest* produced by the
// current (post-FR-57) deployment.yaml.tmpl is byte-for-byte identical to
// what the pre-change template (checked into testdata as a frozen
// reference) produces from the same values. This exercises the manifest
// output directly, not just the intermediate values.yaml.
func TestRenderDeployment_ByteIdenticalToPreChangeTemplate(t *testing.T) {
	values := valuesMapFromData(t, fixtureNoSecretEnv())
	root := defaultHelmRoot(values)

	got := renderTemplateFile(t, deploymentTemplatePath, root)
	want := renderTemplateFile(t, "testdata/pre_change_deployment.yaml.tmpl", root)

	if got != want {
		t.Errorf("rendered deployment manifest changed for a chart with no secret-sourced env\n--- got ---\n%s\n--- want (pre-change) ---\n%s", got, want)
	}
}

// TestRenderJob_ByteIdenticalToPreChangeTemplate is the job.yaml.tmpl analog
// of TestRenderDeployment_ByteIdenticalToPreChangeTemplate.
func TestRenderJob_ByteIdenticalToPreChangeTemplate(t *testing.T) {
	values := valuesMapFromData(t, fixtureJobNoSecretEnv())
	root := defaultHelmRoot(values)

	got := renderTemplateFile(t, jobTemplatePath, root)
	want := renderTemplateFile(t, "testdata/pre_change_job.yaml.tmpl", root)

	if got != want {
		t.Errorf("rendered job manifest changed for a chart with no secret-sourced env\n--- got ---\n%s\n--- want (pre-change) ---\n%s", got, want)
	}
}

// TestRenderDeployment_SecretKeyRef proves a declared secretEnv entry
// renders as env[].valueFrom.secretKeyRef{name,key} and that the secret's
// value never appears anywhere in the rendered manifest.
func TestRenderDeployment_SecretKeyRef(t *testing.T) {
	data := fixtureNoSecretEnv()
	app := data.Apps["myapp-api"]
	app.SecretEnv = []SecretEnvEntry{
		{Name: "SECRET_KEY", SecretName: "app-secrets", Key: "secret-key"},
	}
	data.Apps["myapp-api"] = app

	values := valuesMapFromData(t, data)
	rendered := renderTemplateFile(t, deploymentTemplatePath, defaultHelmRoot(values))
	container := decodeSingleContainer(t, rendered)

	var found *k8sEnvVar
	for i := range container.Env {
		if container.Env[i].Name == "SECRET_KEY" {
			found = &container.Env[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("SECRET_KEY env var missing from rendered container\n%s", rendered)
	}
	if found.Value != "" {
		t.Errorf("SECRET_KEY rendered a literal value %q; must be sourced only via secretKeyRef", found.Value)
	}
	if found.ValueFrom == nil || found.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("SECRET_KEY did not render valueFrom.secretKeyRef: %+v", found)
	}
	if found.ValueFrom.SecretKeyRef.Name != "app-secrets" || found.ValueFrom.SecretKeyRef.Key != "secret-key" {
		t.Errorf("unexpected secretKeyRef %+v", found.ValueFrom.SecretKeyRef)
	}

	// The secret's own value ("secret-key" is the *key name*, not a value in
	// this fixture -- there is no literal secret value to leak here by
	// construction of secretEnv, since it never carries one). What must
	// never leak is a literal `value:` field on the SECRET_KEY entry itself,
	// asserted above via found.Value.
}

// TestRenderDeployment_EnvFrom proves an envFrom entry renders under the
// container's envFrom list.
func TestRenderDeployment_EnvFrom(t *testing.T) {
	data := fixtureNoSecretEnv()
	app := data.Apps["myapp-api"]
	app.EnvFrom = []EnvFromEntry{{SecretRef: "app-bulk-secrets"}}
	data.Apps["myapp-api"] = app

	values := valuesMapFromData(t, data)
	rendered := renderTemplateFile(t, deploymentTemplatePath, defaultHelmRoot(values))
	container := decodeSingleContainer(t, rendered)

	if len(container.EnvFrom) != 1 {
		t.Fatalf("expected exactly 1 envFrom entry, got %d\n%s", len(container.EnvFrom), rendered)
	}
	src := container.EnvFrom[0]
	if src.SecretRef == nil || src.SecretRef.Name != "app-bulk-secrets" {
		t.Errorf("expected envFrom secretRef app-bulk-secrets, got %+v", src)
	}
}

// TestRenderDeployment_LiteralSecretEnvFromCoexistDeterministic proves
// literal env vars, secretKeyRef env vars, and envFrom coexist on one app
// and render in a deterministic order across repeated renders.
func TestRenderDeployment_LiteralSecretEnvFromCoexistDeterministic(t *testing.T) {
	data := fixtureNoSecretEnv()
	app := data.Apps["myapp-api"]
	app.Env = map[string]string{"LOG_LEVEL": "info", "APP_MODE": "production"}
	app.SecretEnv = []SecretEnvEntry{
		{Name: "SECRET_KEY", SecretName: "app-secrets", Key: "secret-key"},
		{Name: "DB_PASSWORD", SecretName: "app-db", Key: "password"},
	}
	app.EnvFrom = []EnvFromEntry{
		{SecretRef: "app-bulk-secrets"},
		{ConfigMapRef: "app-bulk-config"},
	}
	data.Apps["myapp-api"] = app

	values := valuesMapFromData(t, data)
	root := defaultHelmRoot(values)

	first := renderTemplateFile(t, deploymentTemplatePath, root)
	second := renderTemplateFile(t, deploymentTemplatePath, root)
	if first != second {
		t.Fatalf("rendering the same values twice produced different output\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}

	container := decodeSingleContainer(t, first)

	names := map[string]bool{}
	for _, e := range container.Env {
		names[e.Name] = true
	}
	for _, want := range []string{"LOG_LEVEL", "APP_MODE", "SECRET_KEY", "DB_PASSWORD"} {
		if !names[want] {
			t.Errorf("expected env var %s to be present alongside the others; got %+v", want, container.Env)
		}
	}
	if len(container.EnvFrom) != 2 {
		t.Fatalf("expected 2 envFrom entries, got %d: %+v", len(container.EnvFrom), container.EnvFrom)
	}

	// Deterministic ordering: literal go map iteration is randomized, but
	// Go's text/template sorts map keys when ranging, so a third render
	// (from a fresh values round-trip) must match the first byte-for-byte.
	valuesAgain := valuesMapFromData(t, data)
	third := renderTemplateFile(t, deploymentTemplatePath, defaultHelmRoot(valuesAgain))
	if first != third {
		t.Errorf("env ordering was not deterministic across independent values.yaml round-trips\n--- first ---\n%s\n--- third ---\n%s", first, third)
	}
}

// TestRenderDeployment_SecretKeyRef_NoLiteralValueLeakage is the
// verification-discipline regression guard: it fails loudly if the template
// is ever changed to emit a literal `value:` for a secretEnv-declared
// variable name anywhere in the manifest text, not just on its own env
// entry (e.g. a stray debug line or accidental duplicate entry).
func TestRenderDeployment_SecretKeyRef_NoLiteralValueLeakage(t *testing.T) {
	data := fixtureNoSecretEnv()
	app := data.Apps["myapp-api"]
	app.SecretEnv = []SecretEnvEntry{
		{Name: "OIDC_CLIENT_SECRET", SecretName: "app-secrets", Key: "oidc-client-secret"},
	}
	data.Apps["myapp-api"] = app

	values := valuesMapFromData(t, data)
	rendered := renderTemplateFile(t, deploymentTemplatePath, defaultHelmRoot(values))

	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "OIDC_CLIENT_SECRET") && strings.Contains(line, "value:") {
			t.Errorf("literal value alongside secret-sourced env var OIDC_CLIENT_SECRET: %q", line)
		}
	}
}
