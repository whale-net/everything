package workshop

import "context"

// VerifyResult reports whether the addon's Workshop source still matches the
// content version a cache entry was built from.
type VerifyResult struct {
	Changed        bool
	ContentVersion string // the source's current version/timestamp
}

// VerifyWorkshopItem checks the addon's Workshop source (via SteamCMD
// workshop_status/workshop_download_item verify semantics against the same
// SteamCMD container path the orchestrator already drives) and reports
// whether its content version has changed since knownContentVersion.
//
// This primitive backs both the install-time verify/cache-refresh flow
// (#2184 FR8/FR9) and the Admin on-demand verify RPC, so it is deliberately
// independent of any install-flow state: it takes exactly the identifiers
// it needs and returns a plain result, with no side effects on the
// orchestrator's in-progress-download tracking or RMQ publishing.
//
// TODO(#2184 Implementation phase): drive the SteamCMD verify container via
// do.dockerClient (mirroring HandleDownloadCommand's container lifecycle),
// parse the reported content version, and compare it against
// knownContentVersion. A SteamCMD/RPC failure here must be surfaced as an
// error so callers can fall back to a plain download rather than failing
// the install (see Testing: "verify RPC/SteamCMD failure -> falls back to a
// plain download rather than failing the install").
func (do *DownloadOrchestrator) VerifyWorkshopItem(ctx context.Context, workshopID, steamAppID, knownContentVersion string) (VerifyResult, error) {
	return VerifyResult{}, nil
}
