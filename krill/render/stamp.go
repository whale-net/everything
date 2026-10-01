package render

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SourceStamp identifies the krill data a doc set was rendered from: the
// Product's SCD2 revision and the latest change time across its spec
// entities. It is deterministic for unchanged data (unlike the render
// timestamp), so two renders of the same data carry identical stamps.
type SourceStamp struct {
	Revision string
	Time     time.Time
}

// StampSource is an optional Source capability reporting a Product's
// latest source change time. A Source without it stamps the zero time.
type StampSource interface {
	ProductSourceTime(ctx context.Context, productID uuid.UUID) (time.Time, error)
}

var stampRe = regexp.MustCompile(`<!-- krill-source: revision=(\S+) time=(\S+) -->`)

// String renders s as the one-line comment placed under each file's header.
func (s SourceStamp) String() string {
	return fmt.Sprintf("<!-- krill-source: revision=%s time=%s -->", s.Revision, s.Time.UTC().Format(time.RFC3339Nano))
}

// ParseStamp extracts the source stamp from a rendered file's content.
func ParseStamp(content string) (SourceStamp, bool) {
	m := stampRe.FindStringSubmatch(content)
	if m == nil {
		return SourceStamp{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, m[2])
	if err != nil {
		return SourceStamp{}, false
	}
	return SourceStamp{Revision: m[1], Time: t}, true
}

// stampFile inserts s on its own line right after content's provenance
// header comment.
func stampFile(content string, s SourceStamp) string {
	const end = "-->\n"
	i := strings.Index(content, end)
	if i < 0 {
		return content
	}
	i += len(end)
	return content[:i] + s.String() + "\n" + content[i:]
}

// ErrNewerCommittedSource is returned by CheckNotNewer when committed docs
// were rendered from newer source data than the data being rendered now.
var ErrNewerCommittedSource = errors.New("committed docs carry a newer source stamp than the data being rendered")

// Stale returns the relative paths (sorted) whose committed content under
// dir differs from files, ignoring the render timestamp. Implemented in
// check.go.
var _ = Stale
