package workshop

import (
	"fmt"
	"net/url"
	"strings"
)

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
// they are not items and not errors.
//
// Identical Workshop IDs within the input are de-duplicated: the first
// occurrence is kept as a normal successful entry, and later occurrences of
// the same ID are marked as a distinct non-fatal outcome (an Err noting which
// earlier line already resolved to that ID) rather than being resolved again
// and producing a duplicate addon.
func ParseWorkshopEntries(input string) []ParsedEntry {
	lines := strings.Split(input, "\n")

	var entries []ParsedEntry
	// firstLineForID maps a resolved Workshop ID to the 1-based line number
	// (among non-blank lines) that first produced it.
	firstLineForID := make(map[string]int)
	lineNumber := 0

	for _, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}
		lineNumber++

		workshopID, err := ParseWorkshopEntry(trimmed)
		if err != nil {
			entries = append(entries, ParsedEntry{RawInput: trimmed, Err: err})
			continue
		}

		if firstLine, seen := firstLineForID[workshopID]; seen {
			entries = append(entries, ParsedEntry{
				RawInput: trimmed,
				Err:      fmt.Errorf("duplicate of line %d", firstLine),
			})
			continue
		}

		firstLineForID[workshopID] = lineNumber
		entries = append(entries, ParsedEntry{RawInput: trimmed, WorkshopID: workshopID})
	}

	return entries
}

// ParseWorkshopEntry resolves a single trimmed line to a Workshop ID,
// auto-detecting raw numeric IDs vs. steamcommunity.com Workshop URLs.
func ParseWorkshopEntry(line string) (workshopID string, err error) {
	line = strings.TrimSpace(line)

	if isAllDigits(line) {
		return line, nil
	}

	if looksLikeURL(line) {
		return parseWorkshopURL(line)
	}

	return "", fmt.Errorf("not a numeric Workshop ID or recognized Workshop URL")
}

// isAllDigits reports whether s is non-empty and consists solely of ASCII
// digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// looksLikeURL reports whether s appears to be a URL (with or without a
// scheme) rather than a bare token, so we know to route it through URL
// parsing rather than rejecting it outright.
func looksLikeURL(s string) bool {
	return strings.Contains(s, "steamcommunity.com") || strings.Contains(s, "://")
}

// parseWorkshopURL parses a (possibly scheme-less) URL and extracts the
// numeric "id" query parameter, requiring the host to be a steamcommunity.com
// variant.
func parseWorkshopURL(line string) (string, error) {
	candidate := line
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}

	parsed, err := url.Parse(candidate)
	if err != nil {
		return "", fmt.Errorf("not a numeric Workshop ID or recognized Workshop URL")
	}

	host := strings.ToLower(parsed.Hostname())
	if host != "steamcommunity.com" && !strings.HasSuffix(host, ".steamcommunity.com") {
		return "", fmt.Errorf("not a numeric Workshop ID or recognized Workshop URL")
	}

	idParam := parsed.Query().Get("id")
	if idParam == "" {
		return "", fmt.Errorf("URL is missing an id parameter")
	}
	if !isAllDigits(idParam) {
		return "", fmt.Errorf("id parameter is not numeric")
	}

	return idParam, nil
}
