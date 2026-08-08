package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/whale-net/everything/tools/helm"
)

func TestReadChartLockfile_BuildsAndPrints(t *testing.T) {
	charts, fs, bazel := makeTestHelmCharts()
	var target *fakeHelmChart
	for i := range charts {
		if charts[i].name == "helm-manmanv2-control-services" {
			target = &charts[i]
		}
	}
	if target == nil {
		t.Fatal("fixture missing helm-manmanv2-control-services")
	}

	chartDir := filepath.Join(fakeWorkspaceRoot, "bazel-bin", target.pkg, target.name+"_chart", strings.TrimPrefix(target.name, "helm-"))
	lockfileContent := `{"chart_name":"manmanv2-control-services","images":[]}`
	fs.add(filepath.Join(chartDir, helm.LockfileFileName), []byte(lockfileContent))

	// Register the bazel build call the command issues before reading.
	bazel.calls = append(bazel.calls, fakeBazelCall{
		argsContain: []string{"build", "//" + target.pkg + ":chart"},
		output:      "",
	})

	withFS(fs, func() {
		withBazel(bazel, func() {
			withWorkspace(fakeWorkspaceRoot, func() {
				stdout, _, err := runTest([]string{"read-chart-lockfile", "helm-manmanv2-control-services"})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !strings.Contains(stdout, lockfileContent) {
					t.Errorf("expected lockfile content in output, got: %s", stdout)
				}
				var sawBuild bool
				for _, call := range bazel.recorded {
					if len(call) > 0 && call[0] == "build" {
						sawBuild = true
					}
				}
				if !sawBuild {
					t.Error("expected a `bazel build` call before reading the lockfile")
				}
			})
		})
	})
}

func TestReadChartLockfile_SkipBuild(t *testing.T) {
	charts, fs, bazel := makeTestHelmCharts()
	var target *fakeHelmChart
	for i := range charts {
		if charts[i].name == "helm-manmanv2-control-services" {
			target = &charts[i]
		}
	}
	if target == nil {
		t.Fatal("fixture missing helm-manmanv2-control-services")
	}

	chartDir := filepath.Join(fakeWorkspaceRoot, "bazel-bin", target.pkg, target.name+"_chart", strings.TrimPrefix(target.name, "helm-"))
	lockfileContent := `{"chart_name":"manmanv2-control-services","images":[]}`
	fs.add(filepath.Join(chartDir, helm.LockfileFileName), []byte(lockfileContent))

	withFS(fs, func() {
		withBazel(bazel, func() {
			withWorkspace(fakeWorkspaceRoot, func() {
				stdout, _, err := runTest([]string{"read-chart-lockfile", "helm-manmanv2-control-services", "--skip-build"})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !strings.Contains(stdout, lockfileContent) {
					t.Errorf("expected lockfile content in output, got: %s", stdout)
				}
				for _, call := range bazel.recorded {
					if len(call) > 0 && call[0] == "build" {
						t.Errorf("--skip-build must not invoke `bazel build`, but recorded: %v", call)
					}
				}
			})
		})
	})
}

func TestReadChartLockfile_UnknownChart(t *testing.T) {
	_, fs, bazel := makeTestHelmCharts()

	withFS(fs, func() {
		withBazel(bazel, func() {
			withWorkspace(fakeWorkspaceRoot, func() {
				_, _, err := runTest([]string{"read-chart-lockfile", "no-such-chart", "--skip-build"})
				if err == nil {
					t.Fatal("expected error for unknown chart name")
				}
			})
		})
	})
}
