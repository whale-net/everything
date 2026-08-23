package cmd

import (
	"context"
	"fmt"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// resolveVersion is issue #829's fix, since the AR-5 cutover: AllocateVersion
// is the shared decision every version-resolution call site (plan.go's
// assignVersions, build_helm.go's build-helm-chart, release_charts.go's
// releaseCharts) goes through instead of calling autoIncrementVersion/
// autoIncrementHelmVersion directly.
//
// When the registry isn't opted in (client is nil), this is exactly the old
// behavior: call tagFallback. When it is opted in, every domain allocates
// unconditionally (there is no more per-domain adoption gate), so any
// AllocateVersion error (network, auth, internal) is fatal here, not a
// fallback: silently reverting to tag-scanning for a domain the registry
// owns is the exact bug issue #829 reports, so a broken registry must fail
// the release rather than mask itself as a tag-based one.
func resolveVersion(ctx context.Context, client pb.ArtifactRegistryClient, kind pb.ArtifactKind, ownerFullName, increment, idempotencyKey string, tagFallback func() (string, error)) (version string, fromRegistry bool, err error) {
	if client == nil {
		v, ferr := tagFallback()
		return v, false, ferr
	}

	resp, aerr := client.AllocateVersion(ctx, &pb.AllocateVersionRequest{
		OwnerFullName:  ownerFullName,
		Kind:           kind,
		Increment:      increment,
		IdempotencyKey: idempotencyKey,
	})
	if aerr != nil {
		return "", false, fmt.Errorf("AllocateVersion: %w", aerr)
	}
	return resp.Version, true, nil
}

// dialVersioningClient returns injected if set (test injection), otherwise
// dials a fresh ArtifactRegistryClient. Unlike every other App Registry dial
// site in this package (AssertApps, RecordBuild, CheckChartHermeticity),
// which treat a dial failure as best-effort and silently skip the call, a
// dial failure here is returned as an error: the caller only reaches this
// helper once it has already decided the registry is opted in, so a failure
// to dial must fail the release the same way any other AllocateVersion error
// does (see resolveVersion) -- not silently fall back to tag-scanning for a
// domain we have no way to confirm isn't at "allocate".
func dialVersioningClient(ctx context.Context, injected pb.ArtifactRegistryClient) (client pb.ArtifactRegistryClient, closeFn func() error, err error) {
	if injected != nil {
		return injected, func() error { return nil }, nil
	}
	c, closeConn, derr := NewArtifactRegistryClient(ctx)
	if derr != nil {
		return nil, nil, fmt.Errorf("connect to App Registry for version resolution: %w", derr)
	}
	return c, closeConn, nil
}

