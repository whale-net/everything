package tools

// SearchToolsName is the reserved meta-tool name (FR8): no configured
// domain server may expose a real tool by this literal name, in any
// agent definition's tool set, bulk or search mode alike. The reservation
// is global, not search-mode-only, and is enforced once for every caller
// in ListToolDefinitions (listdefs.go) -- the shared per-server loop
// every agent definition's turn-1-and-later Tools resolution already
// goes through. whagent-net itself will later expose search_tools as an
// in-process meta-tool (#2669) for search-mode tool loading.
const SearchToolsName = "search_tools"
