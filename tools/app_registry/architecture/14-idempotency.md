# Idempotency

Every write RPC takes a required `idempotency_key`. The server stores
`(key, method) → serialized response`; a repeat of the SAME RPC with the SAME
key returns the original response with an `already_*` flag rather than
re-executing. CI reruns and Temporal activity retries are therefore safe by
construction.

Convention: `<workflow_run_id>-<attempt>-<owner>-<kind>-<verb>` for CI (the
trailing `-<verb>`, e.g. `-begin`/`-record`/`-fail`, distinguishes the
different RPCs a single release leg calls against the same target — see
"Fixed: cross-method replay via a reused key (issue #575)" below); a
client-generated UUID for human promotions.

**Scoped to `(key, method)`, not `key` alone.** Lookups used to be keyed on
`idempotency_key` only — `method` was recorded by `Put` but never consulted
by `Get`. A caller that (by mistake) reused the same key across two
*different* RPCs got the first RPC's stored response silently replayed as
the second RPC's result: no error, because `runIdempotent` cannot tell a
correct replay from a wrong one once it finds *any* row for the key. Get is
now `Get(key, method)` and only reports a hit when both match; a key found
under a different method behaves exactly like no stored response at all
(re-execute), never an error. `idempotency_key`'s uniqueness constraint
(migration 009) moved from `PRIMARY KEY (idempotency_key)` to
`PRIMARY KEY (idempotency_key, method)` to match — every pre-migration row's
key was already globally unique, so it stays unique alongside its own method
with no backfill or data loss.

## Fixed: cross-method replay via a reused key (issue #575)

`release.yml` gave `BeginPublish` and `RecordArtifact` the SAME idempotency
key for a release leg (`<run_id>-<attempt>-<owner>-<kind>`, no verb suffix),
and the same for a chart's `begin-publish`/`artifacts record`. When
`RecordArtifact` ran after `BeginPublish` for the same leg, `Get` found
`BeginPublish`'s already-committed response under that key and
`runIdempotent` treated it as a valid replay of `RecordArtifact` — so
`RecordArtifact`'s actual write (`completePublish`, which sets
`digest`/`state = published`) never ran. This did not surface as an error:
`BeginPublishResponse` and `RecordArtifactResponse` both put an `Artifact` at
proto field 1 (`api_messages_artifact.proto`), so unmarshaling one into the
other's Go struct succeeds silently and the RPC returns `OK`. The artifact
row was left stuck in `publishing` (no digest, no `published_at`) until the
stale-row reaper failed it, long after the release job had already reported
green — see OPERATIONS.md's "A release run didn't complete" for how to spot
a row in this state.

Two independent fixes, both required: (1) `release.yml` now gives
`BeginPublish`/`begin-publish` and `RecordArtifact`/`artifacts record` their
own distinct keys (`-begin` / `-record` suffixes, mirroring `FailPublish`'s
pre-existing `-fail`) for both images and charts, so the two RPCs no longer
share a key at all; (2) the server-side `(key, method)` scoping above means
that even a *future* accidental key reuse across two different RPCs fails
safe — the second call re-executes instead of silently replaying the first
call's response. `BeginPublishBatch` was already unaffected: its
`idempotency_key_prefix` gets a server-side `-intent` suffix
(`BeginPublishBatchRequest.idempotency_key_prefix`'s doc comment) that was
already distinct from the per-leg `BeginPublish` key, by design, so the two
never collided in the first place.

