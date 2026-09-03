// Package deps is a compile-only smoke target for the three net-new
// external Go dependencies M1 needs (see ARCHITECTURE.md § MCP server):
// the official MCP Go SDK, the YouTube Data API v3 client, and the
// YouTube Analytics API v2 client. It imports each package and
// references one symbol from each so `bazel build` actually resolves
// and links them — no product behavior here, that lands in later M1
// tasks (mcp, worker binaries).
package deps

import (
	"reflect"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	youtube "google.golang.org/api/youtube/v3"
	youtubeanalytics "google.golang.org/api/youtubeanalytics/v2"
)

// Symbols is a smoke-test slice referencing one type from each vendored
// package, so gazelle/go vet/the compiler all see a real, non-dead-code
// dependency edge.
var Symbols = []reflect.Type{
	reflect.TypeOf(mcp.Server{}),
	reflect.TypeOf(youtube.Service{}),
	reflect.TypeOf(youtubeanalytics.Service{}),
}
