package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SaveNoteRelationInput is one entry of FR5's relations list -- a typed
// edge from the note being saved to a prior note in the SAME resolved
// thread (FR6/NFR4). See SaveNoteInput.Relations and SaveNote's doc
// comment for the validation/rollback rules.
type SaveNoteRelationInput struct {
	RelatedNoteID uuid.UUID
	RelationType  RelationType
}

// SaveNoteInput is the input to ResearchStore.SaveNote.
type SaveNoteInput struct {
	ChannelID uuid.UUID
	// IdeaID is thread-RESOLUTION input, not the note's own Idea
	// attachment (issue #1940, FR2 Stage 2) -- the note's effective Idea
	// always comes from the resolved thread. With ThreadTitle it is the
	// Idea component of the find-or-create natural key; with ThreadID it
	// must agree with that thread's resolved Idea or SaveNote rejects the
	// call (see SaveNote's doc comment). nil if the note predates an Idea
	// (FR9).
	IdeaID *uuid.UUID
	// ThreadID attaches this note to an existing research_thread (FR4).
	// Exactly one of ThreadID/ThreadTitle must be supplied -- see
	// SaveNote's doc comment for the full resolution/rejection rules.
	ThreadID *uuid.UUID
	// ThreadTitle find-or-creates a thread on the natural key
	// (channel_id, idea_id, lower(trim(title))), delegating to
	// findOrCreateThreadTx (thread.go) run inside SaveNote's OWN
	// transaction -- IdeaID above is the Idea component of that natural
	// key (FR4).
	ThreadTitle string
	// Relations are FR5's typed edges to prior notes, written atomically
	// with this note (same transaction) -- see SaveNote's doc comment.
	// Duplicate (RelatedNoteID, RelationType) entries collapse to one row
	// (the table's PK); this is not an error.
	Relations []SaveNoteRelationInput
	Text      string
	// SourceURL is validated and normalized by SaveNote itself (FR12): nil
	// or a pointer to an empty/whitespace-only string persists as SQL NULL
	// (uncited, FR10); a valid http(s) URL persists trimmed; anything else
	// makes SaveNote return an error with no INSERT attempted.
	SourceURL      *string
	AuthorPersonID uuid.UUID
	IdempotencyKey string
}

// ResearchNoteWithAuthor is a ResearchNote plus its author's display name
// (joined from `person`) -- the shape list_research_notes
// (mcp/tools/research.go, issue #1577) renders, since a Person's display
// name is not a column on research_note itself.
type ResearchNoteWithAuthor struct {
	ResearchNote
	AuthorDisplayName string
}

// ResearchStore covers `research_note` (migration 002, FR9/FR10).
type ResearchStore interface {
	// SaveNote inserts a research_note row, honouring IdempotencyKey
	// (NFR2): a replayed (author, key) pair must not create a duplicate
	// row.
	SaveNote(ctx context.Context, in SaveNoteInput) (ResearchNote, error)

	// GetByID returns the ResearchNote for id, or an error if none exists.
	// Backs save_research_note's WriteRender step (mcp/tools/research.go),
	// which always re-reads from Postgres rather than caching what SaveNote
	// returned (LB4 -- see RegisterWrite's doc, ../mcp/server/registry.go).
	GetByID(ctx context.Context, id uuid.UUID) (ResearchNote, error)

	// ListByChannel returns every ResearchNote for channelID.
	ListByChannel(ctx context.Context, channelID uuid.UUID) ([]ResearchNote, error)

	// GetByIDs resolves every id in ids to its ResearchNote in ONE batched
	// query (never one GetByID call per id) -- backs both
	// mcp/tools/verdict.go's resolveCitedNotes and web/research's
	// renderIdeaDetail cited-notes rendering (FR9/FR16, NFR2), so an Idea
	// with a long verdict history citing many notes issues a single query
	// regardless of how many verdict versions or citations there are. An id
	// with no matching row is simply absent from the returned map (never an
	// error) -- verdict_citation FKs research_note and nothing in this
	// package deletes a research_note, so a missing id is a defensive case,
	// not an expected one; callers decide how to render it. Duplicate ids
	// in the input are deduplicated before querying.
	GetByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]ResearchNote, error)

	// ListFiltered returns ResearchNote rows for channelID, most-recent
	// first, each joined to its author's display name, optionally narrowed
	// to a single ideaID (nil = no filter, matched via the note's resolved
	// thread's Idea) and/or a single threadID (nil = no filter, issue
	// #1940, FR2 Stage 2b/FR8 -- composes with every other filter, never
	// bypasses one), partitioned by cited (source_url IS NOT NULL,
	// cited=true) vs uncited (source_url IS NULL, cited=false, FR10; nil =
	// no cited/uncited filter), optionally restricted by currentOnly (FR8)
	// to rows also present in v_current_research_note -- i.e. excluding
	// any note that is the related_note_id target of a 'supersedes' or
	// 'excludes' relation (being the target of
	// 'caveats'/'follows_up'/'summarizes' does NOT exclude a note; false =
	// no restriction, the pre-FR8 behaviour) -- and bounded by since
	// (inclusive lower bound on created_at, nil = no bound) and before
	// (exclusive upper bound, nil = no bound). limit caps the number of
	// rows returned (<=0 = unbounded); truncated reports whether more
	// matching rows exist beyond limit -- both together implement
	// list_research_notes' and get_channel_overview's since/before/limit
	// pagination entirely in this layer (issue #1808's follow-up: no
	// unbounded fetch-then-filter-in-Go), and currentOnly composes with
	// that pagination rather than bypassing it: truncated is computed over
	// the currentOnly-filtered set, never the raw one. Backs
	// list_research_notes (mcp/tools/research.go, issue #1577).
	ListFiltered(ctx context.Context, channelID uuid.UUID, ideaID, threadID *uuid.UUID, cited *bool, currentOnly bool, since, before *time.Time, limit int) (notes []ResearchNoteWithAuthor, truncated bool, err error)

	// RetiredNoteIDs returns the subset of noteIDs that are a CURRENT
	// target of a 'supersedes' or 'excludes' research_note_relation,
	// mapped to which relation type(s) retired each -- FR10's warning
	// source for a cited-but-retired note (mcp/tools/verdict.go's
	// resolveCitedNotes, web/research's citedResearchNotes). This is the
	// SAME predicate v_current_research_note (migration 016) excludes
	// on -- related_note_id is the target of a 'supersedes' or 'excludes'
	// relation -- just queried directly against research_note_relation
	// rather than through the view, because this needs the per-type detail
	// (which relation(s) retired it) the view's WHERE NOT EXISTS collapses
	// away. There is exactly one definition of "retired" in the codebase;
	// this and the view merely express it at two necessary granularities.
	// A noteID absent from the returned map is live for FR10's purposes
	// (it may still be a 'caveats'/'follows_up'/'summarizes' target --
	// FR7's unrelated, non-retiring distinction). Duplicate ids in the
	// input are deduplicated before querying, and if two different notes
	// each 'supersedes' (or 'excludes') the same target, that relation
	// type appears only once in the target's slice. Resolves the WHOLE
	// noteIDs list in one query (FR16/NFR2), never one query per note.
	RetiredNoteIDs(ctx context.Context, noteIDs []uuid.UUID) (map[uuid.UUID][]RelationType, error)
}

// researchStore implements ResearchStore against `research_note`
// (migration 002).
type researchStore struct{ pool *pgxpool.Pool }

var _ ResearchStore = researchStore{}

// researchNoteColumns reads a ResearchNote's Idea via the note's resolved
// thread (rt.idea_id) rather than research_note.idea_id directly (issue
// #1939, FR2 Stage 2a): store.ResearchNote.IdeaID keeps its exact meaning
// and type (*uuid.UUID) -- only its provenance changes here, so every
// caller in mcp/web continues to compile and behave identically. rt.title
// is also selected (issue #1940, FR2 Stage 2b) so a caller can render a
// note's thread_title without a second list_research_threads call.
// thread_id is still nullable until Stage 3, so researchNoteFrom below
// uses a LEFT JOIN, not an inner join -- an inner join would silently
// drop any row a backfill or a stale writer left with a NULL thread_id.
// Revisit to an inner join at Stage 3. Used with researchNoteFrom for
// every READ query; the INSERT...RETURNING in SaveNote uses the separate
// researchNoteInsertColumns instead (RETURNING cannot reference a
// joined table).
const researchNoteColumns = `rn.id, rn.channel_id, rt.idea_id, rn.thread_id, rt.title, rn.text, rn.source_url, rn.author_person_id, rn.created_at, COALESCE(rn.idempotency_key, '')`

// researchNoteFrom is the FROM clause every READ query pairs with
// researchNoteColumns above.
const researchNoteFrom = `research_note rn LEFT JOIN research_thread rt ON rt.id = rn.thread_id`

// researchNoteInsertColumns mirrors researchNoteColumns' column order but
// reads idea_id directly off research_note (unaliased, no JOIN) -- valid
// only in SaveNote's INSERT...RETURNING, where the row was just written
// with idea_id = the resolved thread's IdeaID (SaveNote's own invariant),
// so echoing research_note.idea_id back here agrees with the join by
// construction and needs no subquery. The `NULL::text` in title's
// position is a placeholder to keep this in column-order lockstep with
// scanResearchNote/researchNoteColumns -- SaveNote overwrites it with the
// already-resolved thread.Title in Go immediately after scanning (the
// INSERT has no research_thread join to read it from directly).
const researchNoteInsertColumns = `id, channel_id, idea_id, thread_id, NULL::text, text, source_url, author_person_id, created_at, COALESCE(idempotency_key, '')`

func scanResearchNote(row pgx.Row) (ResearchNote, error) {
	var n ResearchNote
	err := row.Scan(&n.ID, &n.ChannelID, &n.IdeaID, &n.ThreadID, &n.ThreadTitle, &n.Text, &n.SourceURL, &n.AuthorPersonID, &n.CreatedAt, &n.IdempotencyKey)
	return n, err
}

// validateSourceURL trims raw and, if non-empty, requires it to be a
// well-formed absolute http(s) URL -- rejecting rather than silently
// storing junk (e.g. "not a url", "javascript:...") that would later be
// rendered as "cited". Returns nil (never a pointer to an empty string)
// for an absent/empty/whitespace-only raw, which is what SaveNote persists
// as SQL NULL -- the FR10 uncited case. FR12: this is the single copy of
// the rule both `web`'s save form and `mcp`'s save_research_note rely on
// by calling SaveNote, rather than each validating independently.
func validateSourceURL(raw string) (*string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("source_url is not a well-formed URL: %w", err)
	}
	if !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("source_url must be an absolute http or https URL")
	}
	return &trimmed, nil
}

// SaveNote checks for a prior row with the same (channel, author,
// idempotency_key) triple before inserting -- a replayed call with a
// non-empty IdempotencyKey returns the original row unchanged rather than
// creating a duplicate (NFR2). SourceURL validation (FR12,
// validateSourceURL above) runs first, ahead of the idempotency
// short-circuit below: a replay carrying an invalid source_url errors
// rather than silently returning the original row. That early-return
// happens BEFORE thread resolution or any relation write below, which is
// exactly what makes a replay under the same key a no-op rather than a
// second find-or-create/relation-insert pass (NFR1) -- there is no
// separate idempotency mechanism for those.
//
// Thread resolution (FR4): exactly one of in.ThreadID/in.ThreadTitle must
// be supplied.
//   - in.ThreadID: the thread must exist and belong to in.ChannelID; the
//     note's effective idea_id becomes that thread's IdeaID (Stage 1's
//     invariant: idea_id and thread_id's idea never disagree). If in.IdeaID
//     is also supplied, it must agree with the resolved thread's IdeaID
//     (nil vs non-nil counts as disagreement) -- issue #1940's disagreement
//     rule, since in.IdeaID is thread-RESOLUTION input, not a note's own
//     attachment, and a caller passing both must never have them silently
//     diverge.
//   - in.ThreadTitle: delegates to findOrCreateThreadTx (thread.go) using
//     in.IdeaID as the natural key's Idea component.
//
// Both a bad ThreadID/ThreadTitle combination and every relation
// validation failure below (FR5/FR6/NFR4) are caught INSIDE one
// transaction that also holds the note INSERT: any failure past this
// point rolls the whole call back, so a rejected relation never leaves a
// partial note/relation write behind.
func (s researchStore) SaveNote(ctx context.Context, in SaveNoteInput) (ResearchNote, error) {
	var rawSourceURL string
	if in.SourceURL != nil {
		rawSourceURL = *in.SourceURL
	}
	sourceURL, err := validateSourceURL(rawSourceURL)
	if err != nil {
		return ResearchNote{}, err
	}

	if in.IdempotencyKey != "" {
		existing, err := scanResearchNote(s.pool.QueryRow(ctx, `
			SELECT `+researchNoteColumns+`
			FROM `+researchNoteFrom+`
			WHERE rn.channel_id = $1 AND rn.author_person_id = $2 AND rn.idempotency_key = $3
		`, in.ChannelID, in.AuthorPersonID, in.IdempotencyKey))
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ResearchNote{}, fmt.Errorf("lookup research_note by idempotency key: %w", err)
		}
	}

	haveThreadID := in.ThreadID != nil
	haveThreadTitle := strings.TrimSpace(in.ThreadTitle) != ""
	if haveThreadID == haveThreadTitle {
		return ResearchNote{}, fmt.Errorf("exactly one of thread_id or thread_title must be supplied")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ResearchNote{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var thread ResearchThread
	if haveThreadID {
		thread, err = getThreadByIDTx(ctx, tx, *in.ThreadID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ResearchNote{}, fmt.Errorf("thread %s does not exist", *in.ThreadID)
			}
			return ResearchNote{}, fmt.Errorf("lookup thread: %w", err)
		}
		if thread.ChannelID != in.ChannelID {
			return ResearchNote{}, fmt.Errorf("thread %s does not belong to channel %s", *in.ThreadID, in.ChannelID)
		}
		if in.IdeaID != nil && (thread.IdeaID == nil || *thread.IdeaID != *in.IdeaID) {
			return ResearchNote{}, fmt.Errorf("idea_id %s does not match thread %s's idea", *in.IdeaID, *in.ThreadID)
		}
	} else {
		thread, err = findOrCreateThreadTx(ctx, tx, FindOrCreateThreadInput{
			ChannelID:         in.ChannelID,
			IdeaID:            in.IdeaID,
			Title:             in.ThreadTitle,
			CreatedByPersonID: in.AuthorPersonID,
		})
		if err != nil {
			return ResearchNote{}, fmt.Errorf("find or create thread: %w", err)
		}
	}

	// Validate every relation BEFORE the note INSERT (FR5/FR6): a single
	// invalid entry must leave no note row and no relation rows behind,
	// and validating first avoids ever inserting a note this call is
	// about to reject anyway.
	for _, rel := range in.Relations {
		if !rel.RelationType.Valid() {
			return ResearchNote{}, fmt.Errorf("relation_type %q is not a recognized relation type", rel.RelationType)
		}
		var relatedThreadID *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT thread_id FROM research_note WHERE id = $1`, rel.RelatedNoteID).Scan(&relatedThreadID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ResearchNote{}, fmt.Errorf("related_note_id %s does not exist", rel.RelatedNoteID)
			}
			return ResearchNote{}, fmt.Errorf("lookup related_note_id %s: %w", rel.RelatedNoteID, err)
		}
		if relatedThreadID == nil || *relatedThreadID != thread.ID {
			return ResearchNote{}, fmt.Errorf("related_note_id %s is not in the resolved thread %s", rel.RelatedNoteID, thread.ID)
		}
	}

	note, err := scanResearchNote(tx.QueryRow(ctx, `
		INSERT INTO research_note (channel_id, idea_id, thread_id, text, source_url, author_person_id, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))
		RETURNING `+researchNoteInsertColumns,
		in.ChannelID, thread.IdeaID, thread.ID, in.Text, sourceURL, in.AuthorPersonID, in.IdempotencyKey))
	if err != nil {
		return ResearchNote{}, fmt.Errorf("insert research_note: %w", err)
	}
	title := thread.Title
	note.ThreadTitle = &title

	for _, rel := range in.Relations {
		if rel.RelatedNoteID == note.ID {
			return ResearchNote{}, fmt.Errorf("related_note_id %s must not be the note's own id", rel.RelatedNoteID)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO research_note_relation (note_id, related_note_id, relation_type)
			VALUES ($1, $2, $3)
			ON CONFLICT (note_id, related_note_id, relation_type) DO NOTHING
		`, note.ID, rel.RelatedNoteID, rel.RelationType); err != nil {
			return ResearchNote{}, fmt.Errorf("insert research_note_relation: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ResearchNote{}, fmt.Errorf("commit: %w", err)
	}
	return note, nil
}

// GetByID returns the ResearchNote for id, or an error if none exists.
func (s researchStore) GetByID(ctx context.Context, id uuid.UUID) (ResearchNote, error) {
	note, err := scanResearchNote(s.pool.QueryRow(ctx, `SELECT `+researchNoteColumns+` FROM `+researchNoteFrom+` WHERE rn.id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ResearchNote{}, pgx.ErrNoRows
		}
		return ResearchNote{}, fmt.Errorf("get research_note by id: %w", err)
	}
	return note, nil
}

func (s researchStore) ListByChannel(ctx context.Context, channelID uuid.UUID) ([]ResearchNote, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+researchNoteColumns+` FROM `+researchNoteFrom+` WHERE rn.channel_id = $1 ORDER BY rn.created_at`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list research notes by channel: %w", err)
	}
	defer rows.Close()

	var notes []ResearchNote
	for rows.Next() {
		n, err := scanResearchNote(rows)
		if err != nil {
			return nil, fmt.Errorf("scan research_note: %w", err)
		}
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list research notes by channel: %w", err)
	}
	return notes, nil
}

// GetByIDs resolves ids to their ResearchNote rows in one query (`WHERE id
// = ANY($1)`), deduplicating ids first so a caller can pass a raw union of
// several verdicts' CitedResearchNoteIDs without pre-deduplicating itself.
// An id with no matching row is simply absent from the result map.
func (s researchStore) GetByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]ResearchNote, error) {
	out := make(map[uuid.UUID]ResearchNote, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	seen := make(map[uuid.UUID]struct{}, len(ids))
	unique := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	rows, err := s.pool.Query(ctx, `SELECT `+researchNoteColumns+` FROM `+researchNoteFrom+` WHERE rn.id = ANY($1)`, unique)
	if err != nil {
		return nil, fmt.Errorf("get research_note by ids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		n, err := scanResearchNote(rows)
		if err != nil {
			return nil, fmt.Errorf("scan research_note: %w", err)
		}
		out[n.ID] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get research_note by ids: %w", err)
	}
	return out, nil
}

// researchNoteWithAuthorColumns mirrors researchNoteColumns -- idea_id
// read via rt.idea_id, not rn.idea_id (see researchNoteColumns' doc
// comment, issue #1939), plus rt.title (issue #1940) -- plus the author's
// display name from `person`.
const researchNoteWithAuthorColumns = `rn.id, rn.channel_id, rt.idea_id, rn.thread_id, rt.title, rn.text, rn.source_url, rn.author_person_id, rn.created_at, COALESCE(rn.idempotency_key, ''), COALESCE(p.display_name, '')`

func scanResearchNoteWithAuthor(row pgx.Row) (ResearchNoteWithAuthor, error) {
	var n ResearchNoteWithAuthor
	err := row.Scan(&n.ID, &n.ChannelID, &n.IdeaID, &n.ThreadID, &n.ThreadTitle, &n.Text, &n.SourceURL, &n.AuthorPersonID, &n.CreatedAt, &n.IdempotencyKey, &n.AuthorDisplayName)
	return n, err
}

// ListFiltered joins research_note (or, when currentOnly is true,
// v_current_research_note -- FR8) to person for the author's display name,
// filters by channelID and optionally ideaID/threadID/cited/currentOnly/
// since/before, and orders most-recent first, capped at limit (see
// fetchLimit/paginate, pagination.go). ideaID nil means no Idea filter
// (matched via rt.idea_id, the note's resolved thread's Idea -- issue
// #1939); threadID nil means no thread filter (issue #1940, FR2 Stage
// 2b/FR8) and composes with every other filter rather than replacing it;
// cited nil means no cited/uncited filter -- callers (list_research_notes,
// mcp/tools/research.go) reject a request that sets both cited_only and
// uncited_only before calling this, so cited here is never ambiguous.
//
// currentOnly swaps the FROM source rather than adding a NOT EXISTS
// predicate here: v_current_research_note (migration 016) is the single
// place FR8's "current" definition lives, and selecting from it -- aliased
// rn, exactly like the research_note table it replaces -- keeps every
// other clause (the rt/p joins, the idea_id/threadID/cited/since/before
// filters, fetchLimit/paginate below) byte-for-byte identical to the
// currentOnly false path, so there is no second copy of the exclusion
// predicate here to drift from the view's (FR16/NFR2).
func (s researchStore) ListFiltered(ctx context.Context, channelID uuid.UUID, ideaID, threadID *uuid.UUID, cited *bool, currentOnly bool, since, before *time.Time, limit int) ([]ResearchNoteWithAuthor, bool, error) {
	noteSource := "research_note rn"
	if currentOnly {
		noteSource = "v_current_research_note rn"
	}
	query := `
		SELECT ` + researchNoteWithAuthorColumns + `
		FROM ` + noteSource + `
		LEFT JOIN research_thread rt ON rt.id = rn.thread_id
		JOIN person p ON p.id = rn.author_person_id
		WHERE rn.channel_id = $1`
	args := []any{channelID}

	if ideaID != nil {
		args = append(args, *ideaID)
		query += fmt.Sprintf(" AND rt.idea_id = $%d", len(args))
	}
	if threadID != nil {
		args = append(args, *threadID)
		query += fmt.Sprintf(" AND rn.thread_id = $%d", len(args))
	}
	if cited != nil {
		if *cited {
			query += " AND rn.source_url IS NOT NULL"
		} else {
			query += " AND rn.source_url IS NULL"
		}
	}
	if since != nil {
		args = append(args, *since)
		query += fmt.Sprintf(" AND rn.created_at >= $%d", len(args))
	}
	if before != nil {
		args = append(args, *before)
		query += fmt.Sprintf(" AND rn.created_at < $%d", len(args))
	}
	query += " ORDER BY rn.created_at DESC"
	args = append(args, fetchLimit(limit))
	query += fmt.Sprintf(" LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list research notes filtered: %w", err)
	}
	defer rows.Close()

	var notes []ResearchNoteWithAuthor
	for rows.Next() {
		n, err := scanResearchNoteWithAuthor(rows)
		if err != nil {
			return nil, false, fmt.Errorf("scan research_note with author: %w", err)
		}
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list research notes filtered: %w", err)
	}
	notes, truncated := paginate(notes, limit)
	return notes, truncated, nil
}

// RetiredNoteIDs queries research_note_relation directly (SELECT DISTINCT
// related_note_id, relation_type WHERE related_note_id = ANY(noteIDs) AND
// relation_type IN ('supersedes', 'excludes')) -- the identical predicate
// v_current_research_note's WHERE NOT EXISTS clause negates (migration
// 016), so "retired" has exactly one definition in the codebase even
// though this method and that view read it at different granularities
// (this needs which type(s) retired a note; the view only needs
// current-vs-not). DISTINCT collapses the case where two different notes
// each 'supersedes' (or 'excludes') the same target down to one entry per
// relation type in that target's slice.
func (s researchStore) RetiredNoteIDs(ctx context.Context, noteIDs []uuid.UUID) (map[uuid.UUID][]RelationType, error) {
	out := make(map[uuid.UUID][]RelationType)
	if len(noteIDs) == 0 {
		return out, nil
	}

	seen := make(map[uuid.UUID]struct{}, len(noteIDs))
	unique := make([]uuid.UUID, 0, len(noteIDs))
	for _, id := range noteIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT related_note_id, relation_type
		FROM research_note_relation
		WHERE related_note_id = ANY($1) AND relation_type IN ('supersedes', 'excludes')
		ORDER BY related_note_id, relation_type
	`, unique)
	if err != nil {
		return nil, fmt.Errorf("query retired research note ids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		var relationType RelationType
		if err := rows.Scan(&id, &relationType); err != nil {
			return nil, fmt.Errorf("scan retired research note relation: %w", err)
		}
		out[id] = append(out[id], relationType)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query retired research note ids: %w", err)
	}
	return out, nil
}
