package tools

import (
	"context"

	"github.com/whale-net/everything/libs/go/whagent"
	"github.com/whale-net/everything/whagent_net/api/persona"
	"github.com/whale-net/everything/whagent_net/session"
)

// idempotencyKey derives the deterministic idempotency_key (FR11, LB4)
// dispatch.go's Dispatch carries as a top-level tool argument for a
// mutating call: a thin, package-local wrapper over
// whagent.DeriveIdempotencyKey so dispatch.go depends on one seam here
// rather than reaching into //libs/go/whagent directly. See that
// function's doc comment for why this must never be regenerated on
// activity retry -- (sessionID, turn, callIndex) alone determines it.
func idempotencyKey(sessionID string, turn, callIndex int) string {
	return whagent.DeriveIdempotencyKey(sessionID, turn, callIndex)
}

// mintCredential mints the whagent-net-signed persona credential (FR10,
// NFR4) one tool call to audience carries: issuer.Issue derives sub/sub_iss
// from sess.OnBehalfOf and act from agentID plus sess.Subject (LB2's shape
// verbatim), scoped to exactly one target server (audience) -- never a
// wildcard or multi-audience token, and never reused across servers
// (ARCHITECTURE.md "Identity and auth chaining"). issuer is worker's own
// in-process *persona.Issuer, constructed from the same signing-key env
// vars api reads (see ARCHITECTURE.md "Issuance mechanism (issue
// #2115)") -- there is no RPC hop here.
func mintCredential(ctx context.Context, issuer *persona.Issuer, sess *session.Session, agentID, audience string) (string, error) {
	return issuer.Issue(ctx, sess, agentID, audience)
}
