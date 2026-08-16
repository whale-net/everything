package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/viewdata"
)

// diffOwnerKey mirrors matrix.ownerKey's convention ("chart:<id>" /
// "app:<id>") without importing the matrix package for it — this file has
// no other use for matrix, and the convention is small and stable enough
// not to be worth a cross-package export.
func diffOwnerKey(a *pb.Artifact) string {
	if a.GetChartId() != "" {
		return "chart:" + a.GetChartId()
	}
	return "app:" + a.GetAppId()
}

// parseSemverTriple parses "v<major>.<minor>.<patch>" (the only version
// shape this registry allocates or validates — see AllocateVersionRequest's
// doc comment). ok is false for anything else, so callers never guess a
// comparison direction from an unparseable value.
func parseSemverTriple(v string) (triple [3]int, ok bool) {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return triple, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return triple, false
		}
		triple[i] = n
	}
	return triple, true
}

// compareSemver returns -1/0/1 for from < to / from == to / from > to, and
// ok == false when either side doesn't parse as v<major>.<minor>.<patch> --
// callers must fall back to viewdata.DiffMismatch rather than guess.
func compareSemver(from, to string) (cmp int, ok bool) {
	f, fok := parseSemverTriple(from)
	t, tok := parseSemverTriple(to)
	if !fok || !tok {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if f[i] != t[i] {
			if f[i] < t[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// buildEnvironmentDiff resolves screen 12's data: the FR-20 SCD2 snapshot
// (via GetEnvironmentState's own `at` window) of both named environments,
// diffed by owner identity. Only entities present in EITHER side appear —
// this is deliberately not the full deployments matrix (every PROMOTABLE
// entity, present or not): a diff that padded in every never-promoted
// entity as "not present on either side" would bury the rows that actually
// matter.
func (app *App) buildEnvironmentDiff(ctx context.Context, fromEnv, toEnv *pb.Environment, at int64) (*viewdata.EnvironmentDiffData, error) {
	fromResp, fromErr := app.registry.Promotion.GetEnvironmentState(ctx, &pb.GetEnvironmentStateRequest{
		EnvironmentKey: fromEnv.GetKey(),
		At:             at,
	})
	toResp, toErr := app.registry.Promotion.GetEnvironmentState(ctx, &pb.GetEnvironmentStateRequest{
		EnvironmentKey: toEnv.GetKey(),
		At:             at,
	})

	chartsResp, err := app.registry.App.ListCharts(ctx, &pb.ListChartsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListCharts: %w", err)
	}
	appsResp, err := app.registry.App.ListApps(ctx, &pb.ListAppsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListApps: %w", err)
	}
	fullNameByKey := make(map[string]string, len(chartsResp.GetCharts())+len(appsResp.GetApps()))
	isChartByKey := make(map[string]bool, len(chartsResp.GetCharts()))
	for _, c := range chartsResp.GetCharts() {
		key := "chart:" + c.GetChartId()
		fullNameByKey[key] = c.GetFullName()
		isChartByKey[key] = true
	}
	for _, a := range appsResp.GetApps() {
		fullNameByKey["app:"+a.GetAppId()] = a.GetFullName()
	}

	fromEntries := make(map[string]*pb.EnvironmentStateEntry)
	if fromErr == nil {
		for _, e := range fromResp.GetEntries() {
			fromEntries[diffOwnerKey(e.GetArtifact())] = e
		}
	}
	toEntries := make(map[string]*pb.EnvironmentStateEntry)
	if toErr == nil {
		for _, e := range toResp.GetEntries() {
			toEntries[diffOwnerKey(e.GetArtifact())] = e
		}
	}

	seen := make(map[string]bool, len(fromEntries)+len(toEntries))
	keys := make([]string, 0, len(fromEntries)+len(toEntries))
	for k := range fromEntries {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range toEntries {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}

	rows := make([]viewdata.DiffRow, 0, len(keys))
	for _, k := range keys {
		fe, te := fromEntries[k], toEntries[k]
		fullName := fullNameByKey[k]
		if fullName == "" {
			// Identity not resolvable in the current ListApps/ListCharts
			// read (e.g. archived between the promotion and this page load)
			// -- fall back to the raw key rather than rendering a blank row.
			fullName = k
		}
		row := viewdata.DiffRow{FullName: fullName, IsChart: isChartByKey[k]}
		if fe != nil {
			row.FromVersion = fe.GetArtifact().GetVersion()
			row.FromDigest = fe.GetArtifact().GetDigest()
		}
		if te != nil {
			row.ToVersion = te.GetArtifact().GetVersion()
			row.ToDigest = te.GetArtifact().GetDigest()
		}
		switch {
		case fe != nil && te == nil:
			row.Status = viewdata.DiffFromOnly
		case fe == nil && te != nil:
			row.Status = viewdata.DiffToOnly
		case row.FromVersion == row.ToVersion:
			row.Status = viewdata.DiffInSync
		default:
			if cmp, ok := compareSemver(row.FromVersion, row.ToVersion); ok {
				if cmp > 0 {
					row.Status = viewdata.DiffFromAhead
				} else {
					row.Status = viewdata.DiffToAhead
				}
			} else {
				row.Status = viewdata.DiffMismatch
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].FullName < rows[j].FullName })

	return &viewdata.EnvironmentDiffData{
		From: fromEnv, To: toEnv, AsOf: at,
		FromErr: fromErr, ToErr: toErr,
		Rows: rows,
	}, nil
}
