package handlers

import (
	"context"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"google.golang.org/protobuf/proto"
)

// runIdempotent executes fn inside a single Registry transaction (see
// repository.Registry.WithTx), replaying a previously stored response for
// key instead of re-executing fn when one exists. This is the mechanism
// backing ARCHITECTURE.md "Idempotency" for every write RPC: the write and
// the idempotency-key row commit or roll back together, so a crash between
// them can never leave a write unrecorded or a key pointing at a stale
// response.
//
// newEmpty must return a fresh zero-value instance of the RPC's response
// message type, used to unmarshal a replayed response.
func runIdempotent(
	ctx context.Context,
	reg repository.Registry,
	key, method string,
	newEmpty func() proto.Message,
	fn func(ctx context.Context, r repository.Registry) (proto.Message, error),
) (resp proto.Message, replayed bool, err error) {
	err = reg.WithTx(ctx, func(ctx context.Context, r repository.Registry) error {
		if raw, found, gerr := r.Idempotency().Get(ctx, key); gerr != nil {
			return gerr
		} else if found {
			msg := newEmpty()
			if uerr := proto.Unmarshal(raw, msg); uerr != nil {
				return uerr
			}
			resp = msg
			replayed = true
			return nil
		}

		out, ferr := fn(ctx, r)
		if ferr != nil {
			return ferr
		}
		raw, merr := proto.Marshal(out)
		if merr != nil {
			return merr
		}
		if perr := r.Idempotency().Put(ctx, key, method, raw); perr != nil {
			return perr
		}
		resp = out
		return nil
	})
	return resp, replayed, err
}
