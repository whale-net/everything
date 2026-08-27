package main

import (
	"context"
	"fmt"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// fetchBoardsPage calls leaflab-api's ListBoards RPC through the
// token-forwarding client (app.api.LeafLab) -- the signed-in user's own
// forwarded access token, never a service account credential (NFR18.1).
// pageToken is passed straight through as FR61's opaque keyset page
// token; an empty token requests the first page.
//
// Scaffold scope (#1330): this is the single call site handlers_boards.go
// and components.BoardsPage build on. Honouring FR61's server-enforced
// page cap end to end, exposing a "load more" continuation from the
// returned page, and computing FR64 elapsed text from the response's
// server_now are Implementation-phase work on this same task.
func (app *App) fetchBoardsPage(ctx context.Context, pageToken string) (*pb.ListBoardsResponse, error) {
	resp, err := app.api.LeafLab.ListBoards(ctx, &pb.ListBoardsRequest{
		Page: &pb.PageRequest{PageToken: pageToken},
	})
	if err != nil {
		return nil, fmt.Errorf("ListBoards: %w", err)
	}
	return resp, nil
}
