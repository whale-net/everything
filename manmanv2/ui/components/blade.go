package components

// BladeData describes one Blade layer. A Blade never opens over another
// Blade (FR10, decision 6) -- callers open at most one at a time.
//
// Scaffold note: this is the skeleton for #2269. Blade itself (blade.templ)
// is written to be liftable into libs/go/htmxui unchanged (NFR1) -- it is
// deliberately defined here in manmanv2/ui/components only because BladeData
// is where the ID/Title/Subtitle values are supplied; nothing about
// BladeData names a manmanv2 domain type.
type BladeData struct {
	ID       string // DOM id, unique per surface
	Title    string
	Subtitle string // e.g. "vanilla on host-01" -- deployment naming per FR2
}

// BladeTab is one entry in a BladeTabs strip (FR13: the Config Editor's
// three fixed tabs). ID is the DOM/data id used to mark the active tab and
// to scope dirty-tracking to the tab's underlying section; Label is the
// display text, rendered verbatim.
type BladeTab struct {
	ID    string
	Label string
}
