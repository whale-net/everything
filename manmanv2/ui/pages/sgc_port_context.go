package pages

import (
	"strings"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// SGCPortContext is the ports editor's FR13/FR14 guidance data (task
// #2098, plan #2080), built by handleSGCDetail through the public API
// (NFR3) and rendered into the editor as JSON: the server's allowed
// host-port ranges per protocol and the set of host ports already in use
// per protocol -- currently allocated (server_ports) OR saved in another
// deployment's port bindings on the same server (a stopped sibling
// deployment's saved binding must not be handed out or flagged
// differently). Guidance only: nothing here ever rejects a save
// (Decision 7); session-start allocation remains the correctness
// backstop.
type SGCPortContext struct {
	Ranges map[string][]*manmanpb.PortRange `json:"ranges"`
	InUse  map[string][]int32               `json:"in_use"`
}

// HasRanges reports whether any allowed range is configured for the
// protocol (SB-1.2: ranges only exist where configured). Exported so the
// ui package's guidance helpers (random pick, warnings) share one
// definition with the page data.
func (c *SGCPortContext) HasRanges(protocol string) bool {
	return c != nil && len(c.Ranges[strings.ToUpper(protocol)]) > 0
}
