package pages

import (
	"strings"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/matrix"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// This file covers #773: a DEPLOY_UNIT_NONE, non-binary ("build-only") app
// has no environment/chart pairing at all (matrix.IsDeployable(app) is
// false for it -- see matrix_test.go's TestIsDeployable), so AppDetail must
// not render the per-environment stat grid or the "Current artifact by
// environment" table for it -- there is no environment for either to ever
// have a version in. A deployable app (standalone image here) must still
// render both, unchanged.

func buildOnlyApp() *pb.App {
	return &pb.App{
		AppId:      "app-build-only",
		Domain:     "demo",
		Name:       "batch-job",
		FullName:   "demo-batch-job",
		AppType:    "job",
		DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE,
	}
}

func deployableApp() *pb.App {
	return &pb.App{
		AppId:      "app-deployable",
		Domain:     "platform",
		Name:       "worker",
		FullName:   "platform-worker",
		AppType:    "worker",
		DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE,
	}
}

func oneDevEnv() []matrix.AppEnvState {
	return []matrix.AppEnvState{
		{Env: &pb.Environment{EnvironmentId: "e1", Key: "dev", Rank: 0}},
	}
}

func TestAppDetail_NonDeployableApp_NoVersionByEnvSection(t *testing.T) {
	data := &matrix.AppDetailData{App: buildOnlyApp(), States: oneDevEnv()}

	html := renderComponent(t, AppDetail(nil, data))

	if strings.Contains(html, "Current artifact by environment") {
		t.Errorf("non-deployable app must not render the per-environment artifact table, got: %s", html)
	}
	if !strings.Contains(html, "Build-only app") {
		t.Errorf("expected a build-only note in place of the version-by-environment section, got: %s", html)
	}
}

func TestAppDetail_DeployableApp_RendersVersionByEnvSection(t *testing.T) {
	data := &matrix.AppDetailData{App: deployableApp(), States: oneDevEnv()}

	html := renderComponent(t, AppDetail(nil, data))

	if !strings.Contains(html, "Current artifact by environment") {
		t.Errorf("deployable app must render the per-environment artifact table, got: %s", html)
	}
	if strings.Contains(html, "Build-only app") {
		t.Errorf("deployable app must not render the build-only note, got: %s", html)
	}
}
