package workshop

// ParsedEntry is the per-line outcome of parsing a pasted block of Workshop
// entries (FR2/FR3). Parsing NEVER returns a top-level error -- one bad line
// must not fail the parse of the rest, so callers range over the returned
// slice and inspect Err per entry instead of a single error return.
type ParsedEntry struct {
	// RawInput is the line exactly as it appeared in the pasted input
	// (trimmed of surrounding whitespace).
	RawInput string
	// WorkshopID is the resolved numeric Steam Workshop ID. Empty when Err
	// is non-nil.
	WorkshopID string
	// Err carries a human-readable reason the line could not be resolved to
	// a Workshop ID. Nil on success.
	Err error
}

// ParseWorkshopEntries splits input on newlines and resolves each non-blank
// line to a Workshop ID, auto-detecting whether the line is a raw numeric ID
// or a steamcommunity.com Workshop URL. Blank lines are skipped entirely --
// they are not items and not errors. Implemented in the Implementation
// phase; see issue #2176.
func ParseWorkshopEntries(input string) []ParsedEntry {
	// TODO(#2176): implement line splitting, auto-detection, and
	// de-duplication per FR2/FR3.
	return nil
}

// ParseWorkshopEntry resolves a single trimmed line to a Workshop ID,
// auto-detecting raw numeric IDs vs. steamcommunity.com Workshop URLs.
// Implemented in the Implementation phase; see issue #2176.
func ParseWorkshopEntry(line string) (workshopID string, err error) {
	// TODO(#2176): implement per-line auto-detection (raw numeric ID vs.
	// steamcommunity.com URL with numeric id query param).
	return "", nil
}
