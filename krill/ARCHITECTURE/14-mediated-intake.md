# Mediated intake (FR9, FR10, NFR2, issue #2546)

`store/mediated.go`'s `MediatedWriteStore.ProposeEntities` and
`api/handlers/mediated.go`'s `ProposeEntitiesHandler` (`POST
/design-sessions/{id}/propose`) are the mediated-intake write path: a
producer-role Agent turns a Requirement Contributor's plain-language
`design_session.opening_submission` into real `feature`/`requirement`
rows, attributing the write to both of them at once.

**Where attribution lives.** Exactly like every other write in this
milestone, a mediated write's attribution lives *only* on the
`revision_event` row it appends — `acting_*` names the producer-role
Agent, `on_behalf_of_*` names the Requirement Contributor. `feature` and
`requirement` (migration `002`) carry no acting/on-behalf-of columns of
their own and never will; FR10's "this is the only place attribution is
durably recorded" is still true for entities created through this path,
not just the ones created through `POST /features`/`POST /requirements`.

**Why NFR2 is structural, not conventional.** `ProposeEntities` opens
exactly one `pgx.Tx`: it inserts every proposed `feature`/`requirement`
row, then appends the one `revision_event` describing all of them, via
`appendRevisionEventTx` (`revision_event.go`'s `Append` factored into a
tx-scoped helper for exactly this reuse) — inside that same transaction. A
crash between the last entity insert and the `revision_event` insert rolls
back the whole transaction, so Postgres itself, not application
discipline, guarantees there is no window where an entity exists without
its attributing event. There is only one code path that writes a
`feature`/`requirement` row on this endpoint, and it is this one — a
second, two-call path (create, then separately `Append`) does not exist
anywhere in this package.

**No lifecycle column, no filtered read.** A `feature`/`requirement` row
`ProposeEntities` creates becomes a current row the instant its `INSERT`
commits — identical to `FeatureStore.Create`/`RequirementStore.Create`.
`GetProductSlice` and every other general slice query (`slice.go`) see it
immediately, with no signoff event anywhere in its `design_session`. M2
adds no staging/lifecycle/status column anywhere to hide an unsigned-off
proposal from a product-wide reader — FR9 requires this to be true by
construction, not by a query-time filter that could later be "fixed" into
existence.

**The `ParentProposalIndex` forward reference.** `MediatedEntityProposal`
lets a `Requirement` proposal name its parent `Feature` either by an
existing `Feature.ID` (`ParentID`, checked via `currentRowExists` like
every other `Create*`) or by the 0-based index of an earlier
`MediatedEntityKindFeature` proposal in the same call (`ParentProposalIndex`)
— the mechanism that lets one mediated write create a `Feature` and its
`Requirement`s together, since that `Feature`'s surrogate id does not
exist anywhere a caller could name it directly before `ProposeEntities`
mints it mid-transaction. A `Feature` proposal's own parent (a
`FeatureSet`) is never itself proposable on this path, so only a
`Requirement` proposal may set `ParentProposalIndex`.

**FR10 enforcement lives in the store, not just the handler.**
`ProposeEntities` rejects `Acting == OnBehalfOf` (all six `Subject` fields
equal) with `ErrMediatedIdentitySame` before ever opening a transaction —
`ProposeEntitiesHandler` maps that to 400 with a message that plainly
states a mediated write requires an agent acting on a contributor's
behalf, but the check itself is unreachable-bypassable only by not calling
`ProposeEntities` at all (e.g. a future MCP tool calling the store
directly still gets it for free).

