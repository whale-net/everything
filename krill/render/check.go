package render

import (
	"fmt"
	"regexp"
	"sort"
)

// ReadFunc reads a committed doc by its path relative to the domain root,
// reporting whether it exists.
type ReadFunc func(rel string) (string, bool)

var renderTimeRe = regexp.MustCompile(`, at \S+\. -->`)

// normalize drops the render timestamp, the only part of a doc that varies
// between two renders of identical data.
func normalize(content string) string {
	return renderTimeRe.ReplaceAllString(content, ". -->")
}

// Stale returns the sorted relative paths whose committed content differs
// from files (or is missing), ignoring the render timestamp.
func Stale(files Files, read ReadFunc) []string {
	var stale []string
	for rel, want := range files.FileMap() {
		got, ok := read(rel)
		if !ok || normalize(got) != normalize(want) {
			stale = append(stale, rel)
		}
	}
	sort.Strings(stale)
	return stale
}

// CheckNotNewer refuses a render whose source data is older than what any
// committed doc was rendered from, so a stale database cannot silently
// overwrite newer docs. Docs without a stamp are never newer.
func CheckNotNewer(files Files, read ReadFunc) error {
	for rel := range files.FileMap() {
		got, ok := read(rel)
		if !ok {
			continue
		}
		if st, ok := ParseStamp(got); ok && st.Time.After(files.Stamp.Time) {
			return fmt.Errorf("%w: %s is stamped %s, live data is %s",
				ErrNewerCommittedSource, rel, st.Time.Format("2006-01-02T15:04:05Z07:00"), files.Stamp.Time.Format("2006-01-02T15:04:05Z07:00"))
		}
	}
	return nil
}
