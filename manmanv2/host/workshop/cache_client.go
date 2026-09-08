package workshop

import (
	"context"
	"net/http"

	pb "github.com/whale-net/everything/manmanv2/protos"
)

// CacheClient fetches Workshop addon content from S3 using short-lived
// presigned URLs obtained from control-api. It holds no S3 credentials.
type CacheClient struct {
	workshopClient pb.WorkshopServiceClient
	serverID       int64
	httpClient     *http.Client
}

// NewCacheClient constructs a CacheClient. If httpClient is nil, http.DefaultClient is used.
func NewCacheClient(workshopClient pb.WorkshopServiceClient, serverID int64, httpClient *http.Client) *CacheClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &CacheClient{
		workshopClient: workshopClient,
		serverID:       serverID,
		httpClient:     httpClient,
	}
}

// TryFetch asks control-api for a presigned GET URL for the given (workshopID,
// contentVersion) pair and, on a cache hit, streams the object from S3 into destDir.
//
// hit == false, err == nil means control-api reports no cache entry for this content —
// this is the ordinary path for a first-ever download and callers must fall back to a
// normal SteamCMD download without treating it as an error.
//
// hit == false, err != nil (e.g. an expired presigned URL, RPC failure, or the RPC being
// Unimplemented against an older control-api) must also degrade to the SteamCMD fallback
// path rather than failing the install.
//
// TODO(#2183 Implementation phase): call GetCacheDownloadURL, stream the response body to
// a temp file under the host data dir, atomically rename it into destDir, report the
// completed read back to control-api via cache_entry_id, and never log the presigned URL.
func (c *CacheClient) TryFetch(ctx context.Context, workshopID, contentVersion, destDir string) (hit bool, err error) {
	return false, nil
}
