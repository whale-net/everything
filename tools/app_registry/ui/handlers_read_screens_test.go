package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/grpc"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// This file covers #654's read-surface cases: as-of/SCD2 (FR-7/FR-8),
// drift-rollup cross-screen agreement (FR-9), empty/failure states (NFR-6),
// chart-detail's two distinct surfaces (FR-24), and the environment-diff
// URL round trip (FR-3). Promotability guardrail (FR-5/NFR-12) and the
// core drift-rollup math are covered directly against the pure
// matrix package (matrix/matrix_test.go); vocabulary/digest (NFR-11,
// FR-33, NFR-4) directly against components (components/read_screens_test.go).

// --- fakes ------------------------------------------------------------

// rsEnvClient is a minimal stand-in for pb.EnvironmentRegistryClient.
type rsEnvClient struct {
	pb.EnvironmentRegistryClient
	environments []*pb.Environment
}

func (f *rsEnvClient) ListEnvironments(ctx context.Context, in *pb.ListEnvironmentsRequest, opts ...grpc.CallOption) (*pb.ListEnvironmentsResponse, error) {
	return &pb.ListEnvironmentsResponse{Environments: f.environments}, nil
}

// rsPromotionClient is a minimal stand-in for pb.PromotionRegistryClient.
// stateByEnv maps environment key -> response; stateErrByEnv maps
// environment key -> a forced error (NFR-6's per-environment failure case).
type rsPromotionClient struct {
	pb.PromotionRegistryClient

	stateByEnv    map[string]*pb.GetEnvironmentStateResponse
	stateErrByEnv map[string]error

	stateCalls []*pb.GetEnvironmentStateRequest
}

func (f *rsPromotionClient) GetEnvironmentState(ctx context.Context, in *pb.GetEnvironmentStateRequest, opts ...grpc.CallOption) (*pb.GetEnvironmentStateResponse, error) {
	f.stateCalls = append(f.stateCalls, in)
	if err, ok := f.stateErrByEnv[in.GetEnvironmentKey()]; ok {
		return nil, err
	}
	if resp, ok := f.stateByEnv[in.GetEnvironmentKey()]; ok {
		return resp, nil
	}
	return &pb.GetEnvironmentStateResponse{}, nil
}

func (f *rsPromotionClient) ListPromotions(ctx context.Context, in *pb.ListPromotionsRequest, opts ...grpc.CallOption) (*pb.ListPromotionsResponse, error) {
	return &pb.ListPromotionsResponse{}, nil
}

// rsAppClient is a minimal stand-in for pb.AppRegistryClient.
type rsAppClient struct {
	pb.AppRegistryClient

	charts []*pb.Chart
	apps   []*pb.App

	setArgoOverrideCalls []*pb.SetChartArgoApplicationNameOverrideRequest
	setArgoOverrideErr   error
}

func (f *rsAppClient) SetChartArgoApplicationNameOverride(ctx context.Context, in *pb.SetChartArgoApplicationNameOverrideRequest, opts ...grpc.CallOption) (*pb.SetChartArgoApplicationNameOverrideResponse, error) {
	f.setArgoOverrideCalls = append(f.setArgoOverrideCalls, in)
	if f.setArgoOverrideErr != nil {
		return nil, f.setArgoOverrideErr
	}
	return &pb.SetChartArgoApplicationNameOverrideResponse{}, nil
}

func (f *rsAppClient) ListCharts(ctx context.Context, in *pb.ListChartsRequest, opts ...grpc.CallOption) (*pb.ListChartsResponse, error) {
	return &pb.ListChartsResponse{Charts: f.charts}, nil
}

func (f *rsAppClient) ListApps(ctx context.Context, in *pb.ListAppsRequest, opts ...grpc.CallOption) (*pb.ListAppsResponse, error) {
	return &pb.ListAppsResponse{Apps: f.apps}, nil
}

func (f *rsAppClient) GetApp(ctx context.Context, in *pb.GetAppRequest, opts ...grpc.CallOption) (*pb.GetAppResponse, error) {
	for _, a := range f.apps {
		if a.GetFullName() == in.GetFullName() || a.GetAppId() == in.GetAppId() {
			return &pb.GetAppResponse{App: a}, nil
		}
	}
	return &pb.GetAppResponse{}, nil
}

// rsArtifactClient is a minimal stand-in for pb.ArtifactRegistryClient.
type rsArtifactClient struct {
	pb.ArtifactRegistryClient

	getArtifactResp      *pb.GetArtifactResponse
	listArtifactPinsErr  error
	listArtifactPinsResp *pb.ListArtifactPinsResponse
}

func (f *rsArtifactClient) GetArtifact(ctx context.Context, in *pb.GetArtifactRequest, opts ...grpc.CallOption) (*pb.GetArtifactResponse, error) {
	return f.getArtifactResp, nil
}

func (f *rsArtifactClient) ListArtifactPins(ctx context.Context, in *pb.ListArtifactPinsRequest, opts ...grpc.CallOption) (*pb.ListArtifactPinsResponse, error) {
	if f.listArtifactPinsErr != nil {
		return nil, f.listArtifactPinsErr
	}
	if f.listArtifactPinsResp == nil {
		return &pb.ListArtifactPinsResponse{}, nil
	}
	return f.listArtifactPinsResp, nil
}

func (f *rsArtifactClient) ListArtifacts(ctx context.Context, in *pb.ListArtifactsRequest, opts ...grpc.CallOption) (*pb.ListArtifactsResponse, error) {
	return &pb.ListArtifactsResponse{}, nil
}

func rsTestApp(env *rsEnvClient, appc *rsAppClient, promo *rsPromotionClient, artifact *rsArtifactClient) *App {
	return &App{
		registry: &RegistryClient{
			Environment: env,
			App:         appc,
			Promotion:   promo,
			Artifact:    artifact,
		},
	}
}

// --- As-of / SCD2 (FR-7, FR-8, NFR-5) ---------------------------------------

func TestHandleDeployments_AsOfSet_QueriesHistoricalStateAndDisablesControls(t *testing.T) {
	envs := []*pb.Environment{{EnvironmentId: "e1", Key: "prod", Rank: 10}}
	apps := []*pb.App{{AppId: "app-1", Domain: "platform", FullName: "platform-worker", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE}}
	promo := &rsPromotionClient{
		stateByEnv: map[string]*pb.GetEnvironmentStateResponse{
			"prod": {Entries: []*pb.EnvironmentStateEntry{
				{Artifact: &pb.Artifact{AppId: "app-1", Version: "v1.0.0", Digest: "sha256:aaaa"}},
			}},
		},
	}
	env := &rsEnvClient{environments: envs}
	appc := &rsAppClient{apps: apps}
	app := rsTestApp(env, appc, promo, &rsArtifactClient{})

	req := httptest.NewRequest(http.MethodGet, "/deployments?at=2024-01-01T00:00", nil)
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleDeployments)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	// FR-7: every environment column's GetEnvironmentState call carries the
	// parsed as-of instant, not 0.
	if len(promo.stateCalls) != 1 || promo.stateCalls[0].GetAt() == 0 {
		t.Fatalf("expected GetEnvironmentState called with a non-zero At, got calls: %+v", promo.stateCalls)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Viewing historical state as of") {
		t.Errorf("expected the on-screen historical-view statement, body: %s", body)
	}
	// FR-8: every mutating control disabled while as-of is active.
	if !strings.Contains(body, "btn-disabled") {
		t.Errorf("expected promote/rollback controls to render disabled while as-of is active, body: %s", body)
	}
	if strings.Contains(body, `href="/promote?`) || strings.Contains(body, `href="/rollback?`) {
		t.Errorf("no promote/rollback control may render as a live, navigable link while as-of is active, body: %s", body)
	}
}

func TestHandleDeployments_AsOfAbsent_CurrentStateAndLiveControls(t *testing.T) {
	envs := []*pb.Environment{{EnvironmentId: "e1", Key: "prod", Rank: 10}}
	apps := []*pb.App{{AppId: "app-1", Domain: "platform", FullName: "platform-worker", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE}}
	promo := &rsPromotionClient{
		stateByEnv: map[string]*pb.GetEnvironmentStateResponse{
			"prod": {Entries: []*pb.EnvironmentStateEntry{
				{Artifact: &pb.Artifact{AppId: "app-1", Version: "v1.0.0", Digest: "sha256:aaaa"}},
			}},
		},
	}
	env := &rsEnvClient{environments: envs}
	appc := &rsAppClient{apps: apps}
	app := rsTestApp(env, appc, promo, &rsArtifactClient{})

	req := httptest.NewRequest(http.MethodGet, "/deployments", nil)
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleDeployments)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(promo.stateCalls) != 1 || promo.stateCalls[0].GetAt() != 0 {
		t.Fatalf("expected GetEnvironmentState called with At=0 (current), got calls: %+v", promo.stateCalls)
	}
	body := w.Body.String()
	if strings.Contains(body, "Viewing historical state as of") {
		t.Errorf("must not render the historical-view statement absent 'at', body: %s", body)
	}
	// A live promote/rollback control (an <a href=...> btn, not btn-disabled)
	// must be present for the fully-roled devUserAuth user.
	if !strings.Contains(body, `href="/rollback?`) {
		t.Errorf("expected a LIVE rollback control (current state, full roles), body: %s", body)
	}
}

// --- Empty/failure states (NFR-6) -------------------------------------------

func TestHandleDeployments_FailedEnvironmentColumn_RendersExplicitErrorNotBlank(t *testing.T) {
	envs := []*pb.Environment{{EnvironmentId: "e1", Key: "prod", Rank: 10}}
	charts := []*pb.Chart{{ChartId: "chart-1", Domain: "platform", FullName: "platform-web"}}
	promo := &rsPromotionClient{
		stateErrByEnv: map[string]error{"prod": context.DeadlineExceeded},
	}
	env := &rsEnvClient{environments: envs}
	appc := &rsAppClient{charts: charts}
	app := rsTestApp(env, appc, promo, &rsArtifactClient{})

	req := httptest.NewRequest(http.MethodGet, "/deployments", nil)
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleDeployments)(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "error loading prod") {
		t.Errorf("expected an explicit per-cell error for the failed environment, body: %s", body)
	}
	if strings.Contains(body, "not promoted") {
		t.Errorf("a failed column must never read as the legitimate 'not promoted' state, body: %s", body)
	}
}

func TestHandleDashboard_FailedEnvironment_NeverRendersHealthy(t *testing.T) {
	envs := []*pb.Environment{{EnvironmentId: "e1", Key: "prod", Rank: 10}}
	promo := &rsPromotionClient{stateErrByEnv: map[string]error{"prod": context.DeadlineExceeded}}
	env := &rsEnvClient{environments: envs}
	app := rsTestApp(env, &rsAppClient{}, promo, &rsArtifactClient{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	app.handleDashboard(w, req)

	body := w.Body.String()
	if strings.Contains(body, "badge-soft badge-success\">Healthy") {
		t.Errorf("a failed environment must never render as Healthy (NFR-6), body: %s", body)
	}
	if !strings.Contains(body, "Unknown") {
		t.Errorf("a failed environment must render an explicit Unknown state, body: %s", body)
	}
}

func TestHandleArtifactDetail_ImageNothingPins_IsEmptyStateNotError(t *testing.T) {
	artifactClient := &rsArtifactClient{
		getArtifactResp: &pb.GetArtifactResponse{
			Artifact: &pb.Artifact{ArtifactId: "art-1", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, Digest: "sha256:bbbb", AppId: "app-1"},
		},
		listArtifactPinsResp: &pb.ListArtifactPinsResponse{}, // nothing pins it -- legitimate empty
	}
	app := rsTestApp(&rsEnvClient{}, &rsAppClient{}, &rsPromotionClient{}, artifactClient)

	req := httptest.NewRequest(http.MethodGet, "/artifacts/sha256:bbbb", nil)
	req.SetPathValue("digest", "sha256:bbbb")
	w := httptest.NewRecorder()
	app.handleArtifactDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Nothing currently pins this image.") {
		t.Errorf("expected the explicit empty-state copy, body: %s", body)
	}
	if strings.Contains(body, "Failed to load pins") {
		t.Errorf("an unpinned image must never render as an error, body: %s", body)
	}
}

func TestHandleArtifactDetail_PinsLookupFails_IsExplicitErrorNotBlank(t *testing.T) {
	artifactClient := &rsArtifactClient{
		getArtifactResp: &pb.GetArtifactResponse{
			Artifact: &pb.Artifact{ArtifactId: "art-1", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, Digest: "sha256:cccc", AppId: "app-1"},
		},
		listArtifactPinsErr: context.DeadlineExceeded,
	}
	app := rsTestApp(&rsEnvClient{}, &rsAppClient{}, &rsPromotionClient{}, artifactClient)

	req := httptest.NewRequest(http.MethodGet, "/artifacts/sha256:cccc", nil)
	req.SetPathValue("digest", "sha256:cccc")
	w := httptest.NewRecorder()
	app.handleArtifactDetail(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "Failed to load pins") {
		t.Errorf("a genuinely failed ListArtifactPins call must render an explicit error, body: %s", body)
	}
	if strings.Contains(body, "Nothing currently pins this image.") {
		t.Errorf("a failed lookup must never be conflated with the legitimate empty state, body: %s", body)
	}
}

func TestHandleArtifactDetail_BinaryArtifact_RendersBinaryTitleAndOwner(t *testing.T) {
	artifactClient := &rsArtifactClient{
		getArtifactResp: &pb.GetArtifactResponse{
			Artifact: &pb.Artifact{ArtifactId: "art-bin", Kind: pb.ArtifactKind_ARTIFACT_KIND_BINARY, Digest: "sha256:bin123", AppId: "app-cli", Version: "v0.3.0"},
		},
	}
	appClient := &rsAppClient{
		apps: []*pb.App{
			{AppId: "app-cli", Domain: "tools", Name: "app-registry", FullName: "tools-app-registry"},
		},
	}
	app := rsTestApp(&rsEnvClient{}, appClient, &rsPromotionClient{}, artifactClient)

	req := httptest.NewRequest(http.MethodGet, "/artifacts/sha256:bin123", nil)
	req.SetPathValue("digest", "sha256:bin123")
	w := httptest.NewRecorder()
	app.handleArtifactDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	if !strings.Contains(body, "Binary artifact") {
		t.Errorf("expected 'Binary artifact' title, got body: %s", body)
	}
	if !strings.Contains(body, "tools-app-registry") {
		t.Errorf("expected owner 'tools-app-registry' in body, got body: %s", body)
	}
}

// --- Drift rollup cross-screen agreement (FR-9) -----------------------------

func TestDashboardAndDriftAudit_DriftCountsAgree(t *testing.T) {
	envs := []*pb.Environment{{EnvironmentId: "e1", Key: "prod", Rank: 10}}
	charts := []*pb.Chart{{ChartId: "chart-1", Domain: "platform", FullName: "platform-web", AppIds: []string{"app-1"}}}
	apps := []*pb.App{{AppId: "app-1", Domain: "platform", FullName: "platform-web-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART}}
	promo := &rsPromotionClient{
		stateByEnv: map[string]*pb.GetEnvironmentStateResponse{
			"prod": {Entries: []*pb.EnvironmentStateEntry{
				{
					Artifact: &pb.Artifact{ChartId: "chart-1", Version: "v2.0.0"},
					Drift: []*pb.DriftEntry{
						{AppId: "app-1", AppFullName: "platform-web-api", ChartPinnedDigest: "sha256:pinned", PromotedDigest: "sha256:override"},
					},
				},
			}},
		},
	}
	env := &rsEnvClient{environments: envs}
	appc := &rsAppClient{charts: charts, apps: apps}
	app := rsTestApp(env, appc, promo, &rsArtifactClient{})

	dashReq := httptest.NewRequest(http.MethodGet, "/", nil)
	dashW := httptest.NewRecorder()
	app.handleDashboard(dashW, dashReq)
	if dashW.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, body = %s", dashW.Code, dashW.Body.String())
	}
	if !strings.Contains(dashW.Body.String(), "1 drifted override(s)") {
		t.Fatalf("expected the dashboard's drift badge to read 1, body: %s", dashW.Body.String())
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/drift-audit", nil)
	auditW := httptest.NewRecorder()
	app.handleDriftAudit(auditW, auditReq)
	if auditW.Code != http.StatusOK {
		t.Fatalf("drift-audit status = %d, body = %s", auditW.Code, auditW.Body.String())
	}
	auditBody := auditW.Body.String()
	if !strings.Contains(auditBody, `badge badge-error ml-2">1<`) {
		t.Errorf("expected the drift-audit count badge to also read 1, agreeing with the dashboard, body: %s", auditBody)
	}
	// The specific drifted entry is identified, not just counted.
	if !strings.Contains(auditBody, "platform-web-api") {
		t.Errorf("expected drift-audit to identify the SPECIFIC drifted app, body: %s", auditBody)
	}
}

// --- Chart detail: two distinct surfaces (FR-24) ----------------------------

func TestHandleChartDetail_PinsAndDeclaredCompositionAreDistinctWhenDisagreeing(t *testing.T) {
	envs := []*pb.Environment{{EnvironmentId: "e1", Key: "prod", Rank: 10}}
	charts := []*pb.Chart{{
		ChartId:  "chart-1",
		Domain:   "platform",
		FullName: "platform-web",
		// Declared composition says only "declared-only-app".
		AppIds: []string{"app-declared"},
	}}
	apps := []*pb.App{
		{AppId: "app-declared", Domain: "platform", FullName: "platform-declared-only"},
		{AppId: "app-pinned", Domain: "platform", FullName: "platform-pinned-only"},
	}
	promo := &rsPromotionClient{
		stateByEnv: map[string]*pb.GetEnvironmentStateResponse{
			"prod": {Entries: []*pb.EnvironmentStateEntry{
				{
					Artifact: &pb.Artifact{
						ChartId: "chart-1",
						Version: "v3.0.0",
						// The BUILD-TIME pin names a DIFFERENT app than the
						// declared composition above -- the two legitimately
						// disagree (FR-24).
						Contains: []*pb.ArtifactLink{
							{AppId: "app-pinned", Version: "v9.9.9", Digest: "sha256:dddd"},
						},
					},
					Promotion: &pb.Promotion{ValidFrom: 1000},
				},
			}},
		},
	}
	env := &rsEnvClient{environments: envs}
	appc := &rsAppClient{charts: charts, apps: apps}
	app := rsTestApp(env, appc, promo, &rsArtifactClient{})

	req := httptest.NewRequest(http.MethodGet, "/charts/platform-web", nil)
	req.SetPathValue("id", "platform-web")
	w := httptest.NewRecorder()
	app.handleChartDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	pinsIdx := strings.Index(body, "Apps published in")
	declaredIdx := strings.Index(body, "Currently declared composition")
	if pinsIdx == -1 || declaredIdx == -1 {
		t.Fatalf("expected both distinctly-labelled sections present, body: %s", body)
	}
	if !strings.Contains(body, "platform-pinned-only") {
		t.Errorf("expected the build-time pinned app to appear under the pins section, body: %s", body)
	}
	if !strings.Contains(body, "platform-declared-only") {
		t.Errorf("expected the declared-composition app to appear under its own section, body: %s", body)
	}

	// Never merged: there is no single table containing both app names in
	// the same <tbody> -- verify by checking the pinned app doesn't appear
	// before the "Currently declared composition" heading's own table
	// (i.e. it's scoped to the first section, not shared).
	declaredSection := body[declaredIdx:]
	if strings.Contains(declaredSection, "platform-pinned-only") {
		t.Errorf("the build-time pin must never be merged into the declared-composition table, body: %s", body)
	}
	pinsSection := body[pinsIdx:declaredIdx]
	if strings.Contains(pinsSection, "platform-declared-only") {
		t.Errorf("the declared composition must never be merged into the build-time pins table, body: %s", body)
	}
}

// --- ArgoCD Application name override editor -------------------------------

// TestHandleChartDetail_ArgoOverrides_DefaultAndExplicitBothRender proves
// buildChartDetail's ArgoOverrides rows (chart_data.go): an environment with
// no explicit override resolves to the "<full_name>-<environment_key>"
// convention and renders a "default" badge, while an environment with an
// explicit override (Chart.argo_application_name_overrides) resolves to
// that value and renders an "override" badge instead -- never the other way
// around.
func TestHandleChartDetail_ArgoOverrides_DefaultAndExplicitBothRender(t *testing.T) {
	envs := []*pb.Environment{
		{EnvironmentId: "e1", Key: "dev", Rank: 0},
		{EnvironmentId: "e2", Key: "prod", Rank: 10},
	}
	charts := []*pb.Chart{{
		ChartId:  "chart-1",
		Domain:   "platform",
		FullName: "platform-web",
		ArgoApplicationNameOverrides: map[string]string{
			"prod": "legacy-platform-app",
		},
	}}
	env := &rsEnvClient{environments: envs}
	appc := &rsAppClient{charts: charts}
	app := rsTestApp(env, appc, &rsPromotionClient{}, &rsArtifactClient{})

	req := httptest.NewRequest(http.MethodGet, "/charts/platform-web", nil)
	req.SetPathValue("id", "platform-web")
	w := httptest.NewRecorder()
	app.handleChartDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	if !strings.Contains(body, "platform-web-dev") {
		t.Errorf("expected dev's default convention name rendered, body: %s", body)
	}
	if !strings.Contains(body, "legacy-platform-app") {
		t.Errorf("expected prod's explicit override value rendered, body: %s", body)
	}
	// The "Resolves to" cell must show the override, not the default
	// convention name -- prod's default ("platform-web-prod") is only
	// allowed to appear as the input's placeholder hint, never as the
	// resolved value itself.
	if !strings.Contains(body, "legacy-platform-app <span") {
		t.Errorf("expected prod's resolved-name cell to render the override immediately before its badge, body: %s", body)
	}
}

// TestHandleChartSetArgoOverride_MissingID_NotFound mirrors
// TestHandleRetryArgoSync_MissingID_NotFound's guard: an empty {id} path
// param short-circuits to a real HTTP 404 before the RPC is attempted.
func TestHandleChartSetArgoOverride_MissingID_NotFound(t *testing.T) {
	appc := &rsAppClient{}
	app := rsTestApp(&rsEnvClient{}, appc, &rsPromotionClient{}, &rsArtifactClient{})

	req := httptest.NewRequest(http.MethodPost, "/charts//argo-override", nil)
	// Deliberately no SetPathValue("id", ...).
	w := httptest.NewRecorder()
	app.handleChartSetArgoOverride(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	if len(appc.setArgoOverrideCalls) != 0 {
		t.Errorf("SetChartArgoApplicationNameOverride calls = %d, want 0 (must not call the RPC with an empty id)", len(appc.setArgoOverrideCalls))
	}
}

// TestHandleChartSetArgoOverride_Success_CallsRPCWithFormFieldsThenRerenders
// proves the submit wiring: the posted form's environment_key,
// argo_application_name, and reason land verbatim on the RPC request
// (FullName resolved from the {id} path param, not the form), and a
// successful call re-renders the SAME chart detail page -- never a
// redirect -- with no OverrideErr banner.
func TestHandleChartSetArgoOverride_Success_CallsRPCWithFormFieldsThenRerenders(t *testing.T) {
	envs := []*pb.Environment{{EnvironmentId: "e1", Key: "prod", Rank: 10}}
	charts := []*pb.Chart{{ChartId: "chart-1", Domain: "platform", FullName: "platform-web"}}
	env := &rsEnvClient{environments: envs}
	appc := &rsAppClient{charts: charts}
	app := rsTestApp(env, appc, &rsPromotionClient{}, &rsArtifactClient{})

	form := url.Values{
		"environment_key":       {"prod"},
		"argo_application_name": {"custom-argo-app"},
		"reason":                {"legacy deployment, doesn't follow convention"},
	}
	req := httptest.NewRequest(http.MethodPost, "/charts/platform-web/argo-override", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "platform-web")
	w := httptest.NewRecorder()
	app.handleChartSetArgoOverride(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(appc.setArgoOverrideCalls) != 1 {
		t.Fatalf("SetChartArgoApplicationNameOverride calls = %d, want 1", len(appc.setArgoOverrideCalls))
	}
	got := appc.setArgoOverrideCalls[0]
	if got.GetFullName() != "platform-web" {
		t.Errorf("FullName = %q, want platform-web (from the {id} path param)", got.GetFullName())
	}
	if got.GetEnvironmentKey() != "prod" {
		t.Errorf("EnvironmentKey = %q, want prod", got.GetEnvironmentKey())
	}
	if got.GetArgoApplicationName() != "custom-argo-app" {
		t.Errorf("ArgoApplicationName = %q, want custom-argo-app", got.GetArgoApplicationName())
	}
	if got.GetReason() != "legacy deployment, doesn't follow convention" {
		t.Errorf("Reason = %q, want the submitted reason", got.GetReason())
	}
	if strings.Contains(w.Body.String(), "alert-error") {
		t.Errorf("a successful save must render no OverrideErr banner; body = %s", w.Body.String())
	}
}

// TestHandleChartSetArgoOverride_RPCFailure_RendersOverrideErrBanner proves
// a failed SetChartArgoApplicationNameOverride call (e.g. server-side
// PermissionDenied for a non-admin session that bypassed the disabled UI
// controls) still re-renders the chart detail page -- HTTP 200, not a raw
// error status -- with the failure surfaced inline via OverrideErr.
func TestHandleChartSetArgoOverride_RPCFailure_RendersOverrideErrBanner(t *testing.T) {
	envs := []*pb.Environment{{EnvironmentId: "e1", Key: "prod", Rank: 10}}
	charts := []*pb.Chart{{ChartId: "chart-1", Domain: "platform", FullName: "platform-web"}}
	env := &rsEnvClient{environments: envs}
	appc := &rsAppClient{
		charts:             charts,
		setArgoOverrideErr: errors.New("rpc error: code = PermissionDenied desc = missing role app-registry-admin"),
	}
	app := rsTestApp(env, appc, &rsPromotionClient{}, &rsArtifactClient{})

	form := url.Values{"environment_key": {"prod"}, "argo_application_name": {"x"}, "reason": {"y"}}
	req := httptest.NewRequest(http.MethodPost, "/charts/platform-web/argo-override", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "platform-web")
	w := httptest.NewRecorder()
	app.handleChartSetArgoOverride(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (a failed save renders inline, not as an HTTP error status)", w.Code, http.StatusOK)
	}
	body := w.Body.String()
	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected an OverrideErr banner; body = %s", body)
	}
	if !strings.Contains(body, "PermissionDenied") {
		t.Errorf("expected the underlying RPC error surfaced; body = %s", body)
	}
	// The rest of the page must still render -- this is a re-render, not a
	// blank error page.
	if !strings.Contains(body, "platform-web") {
		t.Errorf("expected the chart detail page to still render around the error banner; body = %s", body)
	}
}

// --- URL round trip (FR-3) --------------------------------------------------

func TestHandleEnvironmentDiff_URLRoundTrip_AsOfPairAndFiltersReproduceView(t *testing.T) {
	envs := []*pb.Environment{
		{EnvironmentId: "e1", Key: "dev", Rank: 0},
		{EnvironmentId: "e2", Key: "stage", Rank: 5},
		{EnvironmentId: "e3", Key: "prod", Rank: 10},
	}
	promo := &rsPromotionClient{}
	env := &rsEnvClient{environments: envs}
	app := rsTestApp(env, &rsAppClient{}, promo, &rsArtifactClient{})

	req := httptest.NewRequest(http.MethodGet, "/environments/diff?from=dev&to=prod&at=2024-06-01T12:30", nil)
	w := httptest.NewRecorder()
	app.handleEnvironmentDiff(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// The two GetEnvironmentState calls (from + to) must carry the parsed
	// as-of instant from the URL, not 0.
	if len(promo.stateCalls) != 2 {
		t.Fatalf("expected 2 GetEnvironmentState calls (from, to), got %d: %+v", len(promo.stateCalls), promo.stateCalls)
	}
	for _, c := range promo.stateCalls {
		if c.GetAt() == 0 {
			t.Errorf("expected every GetEnvironmentState call to carry the URL's as-of instant, got At=0 for %+v", c)
		}
	}

	// The rendered picker reproduces the exact selection the URL specified
	// -- a pasted link reproduces exactly (FR-3).
	if !strings.Contains(body, `value="dev" selected`) {
		t.Errorf("expected 'dev' selected in the from-picker, body: %s", body)
	}
	if !strings.Contains(body, `value="prod" selected`) {
		t.Errorf("expected 'prod' selected in the to-picker, body: %s", body)
	}
	if !strings.Contains(body, `value="2024-06-01T12:30"`) {
		t.Errorf("expected the as-of input to echo the URL's instant, body: %s", body)
	}
}

// TestHandleEnvironmentDiff_RendersDigestAndProvenance guards NFR-4 (digest
// alongside version) and FR-33 (provenance badge on every screen showing an
// artifact) on screen 12 -- the digest and provenance values are already
// captured in viewdata.DiffRow, but were previously silently dropped at
// render time (see #658).
func TestHandleEnvironmentDiff_RendersDigestAndProvenance(t *testing.T) {
	envs := []*pb.Environment{
		{EnvironmentId: "e1", Key: "dev", Rank: 0},
		{EnvironmentId: "e2", Key: "prod", Rank: 10},
	}
	apps := []*pb.App{{AppId: "app-1", Domain: "platform", FullName: "platform-worker"}}
	promo := &rsPromotionClient{
		stateByEnv: map[string]*pb.GetEnvironmentStateResponse{
			"dev": {Entries: []*pb.EnvironmentStateEntry{
				{Artifact: &pb.Artifact{
					AppId:      "app-1",
					Version:    "v1.0.0",
					Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Provenance: pb.ArtifactProvenance_ARTIFACT_PROVENANCE_ADOPTED,
				}},
			}},
			"prod": {Entries: []*pb.EnvironmentStateEntry{
				{Artifact: &pb.Artifact{
					AppId:      "app-1",
					Version:    "v1.0.0",
					Digest:     "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					Provenance: pb.ArtifactProvenance_ARTIFACT_PROVENANCE_OBSERVED,
				}},
			}},
		},
	}
	env := &rsEnvClient{environments: envs}
	appc := &rsAppClient{apps: apps}
	app := rsTestApp(env, appc, promo, &rsArtifactClient{})

	req := httptest.NewRequest(http.MethodGet, "/environments/diff?from=dev&to=prod", nil)
	w := httptest.NewRecorder()
	app.handleEnvironmentDiff(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// NFR-4: digest renders alongside version, truncated.
	if !strings.Contains(body, "sha256:aaaaaaaaaaaa") {
		t.Errorf("expected the from side's truncated digest to render, body: %s", body)
	}
	if !strings.Contains(body, "sha256:bbbbbbbbbbbb") {
		t.Errorf("expected the to side's truncated digest to render, body: %s", body)
	}
	// FR-33: adopted provenance is tagged; observed is not (matching every
	// other screen's convention of only flagging the ADOPTED case).
	if strings.Count(body, "Adopted") != 1 {
		t.Errorf("expected exactly one 'Adopted' provenance badge (from side only), body: %s", body)
	}
}

func TestFaviconRoute(t *testing.T) {
	mux := http.NewServeMux()
	app := &App{}
	app.setupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Header().Get("Content-Type"); got != "image/x-icon" {
		t.Errorf("Content-Type = %q, want image/x-icon", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("Cache-Control = %q, want public, max-age=86400", got)
	}
	if len(w.Body.Bytes()) == 0 {
		t.Errorf("expected non-empty favicon body")
	}
}
