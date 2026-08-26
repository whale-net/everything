// Package viewdata holds the data shapes screens need beyond raw proto
// messages — the same split as app_registry/ui/viewdata: pure data shaping
// only, no gRPC calls of its own, so both main (which does the fetching) and
// pages (which renders) can import it without an import cycle.
package viewdata

import (
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// BoardsData is everything the boards list screen needs. Err is non-nil
// when ListBoards failed; when Err is nil, Boards contains the fetched
// list (possibly empty). NextPageToken carries the keyset pagination token
// from FR61 — a non-empty token means more results are available.
type BoardsData struct {
	Boards        []*pb.BoardInfo
	Err           error
	NextPageToken string
}
