package main

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/libs/go/htmxui"
)

// buildHead is krill's htmxbase CustomHead: the no-FOUC theme bootstrap,
// then the pinned Tailwind browser build, then the daisyUI stylesheet,
// then htmxui's ThemesCSS -- in that order.
//
// Load-order trap (htmxui ARCHITECTURE §10): ThemesCSS must load *after*
// the daisyUI stylesheet. htmxbase renders CustomCSS before CustomHead,
// so ThemesCSS must go in CustomHead, never CustomCSS -- loading it
// first makes the palette override silently lose to daisyUI's defaults
// with no error anywhere. templ_render_test.go guards the order.
//
// htmx core (4.x) and Alpine (3.x) are loaded by htmxbase's own base
// layout, before CustomHead, so any htmx extension script appended here
// is already ordered after core.
//
// No SSE extension in this PR: krill's ops console uses a
// self-terminating poll instead, and wiring //libs/go/htmxsse would mean
// a RabbitMQ dependency krill does not have today.
func buildHead() string {
	return fmt.Sprintf(`<script>
(function(){var KEY=%q;var t=null;try{t=localStorage.getItem(KEY);}catch(e){t=null;}
if(!t){t=window.matchMedia('(prefers-color-scheme: dark)').matches?'night':'light';}
document.documentElement.setAttribute('data-theme',t);})();
</script>
<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4.3.3/dist/index.global.js"></script>
<style type="text/tailwindcss">
@import "tailwindcss";
</style>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui@5.6.18/daisyui.css">
<style>%s</style>
<style>%s</style>
<script>%s</script>
<script>%s</script>
<script>%s</script>`, htmxui.ThemeSwitcherStorageKey, htmxui.ThemesCSS, markdownCSS, relativeAgeScript, leaseCountdownScript, copyTaskIdScript)
}

// leaseCountdownScript rewrites every board card's lease <time> into the
// relative form FR f6b62cc7 asks for -- "Lease in 18 min" while the claim
// holds, "Lease expired 6 min ago" once it does not.
//
// It reads the absolute instant off the element's `datetime` attribute,
// never off text the server rendered, for the reason NFR 7b497d92 gives:
// the board is a fragment the Refresh button and the scope control
// re-request, so a relative string inside it would be as old as the
// response and would differ between two identical reads. Deriving it here
// -- in the document head, outside every fragment -- also means one
// implementation for all three task views, and re-running after each swap
// picks up a Refresh's new instants without a reload.
//
// Without JavaScript the element keeps the absolute instant the server
// put in it, which is why that instant is the element's own content rather
// than an empty node: the operator still sees when the lease runs out.
//
// Both listeners hang off `document`, never `document.body`: htmxbase
// renders this from CustomHead, so a classic inline script here runs while
// the parser is still inside <head> and document.body is still null. htmx
// events bubble, so document sees every swap regardless.
//
// The swap listener is named `htmx:after:swap` -- the event htmx 4.0.0
// actually dispatches. htmx 4 renamed its lifecycle events to the colon
// form, so the 1.x camelCase name binds a listener that never fires.
const leaseCountdownScript = `
(function(){
function span(ms){
var s=Math.max(1,Math.round(ms/1000));
if(s<60){return s+' second'+(s===1?'':'s');}
var m=Math.round(s/60);if(m<60){return m+' minute'+(m===1?'':'s');}
var h=Math.round(m/60);if(h<24){return h+' hour'+(h===1?'':'s');}
return Math.round(h/24)+' days';
}
function upgrade(root){
var nodes=(root||document).querySelectorAll('time[data-krill="task-lease"][datetime]');
for(var i=0;i<nodes.length;i++){
var t=Date.parse(nodes[i].getAttribute('datetime'));
if(isNaN(t)){continue;}
var left=t-Date.now();
nodes[i].textContent=left>=0?('Lease in '+span(left)):('Lease expired '+span(-left)+' ago');
}
}
document.addEventListener('DOMContentLoaded',function(){upgrade(document);});
document.addEventListener('htmx:after:swap',function(e){upgrade(e.target);});
})();`

// markdownCSS gives goldmark-rendered markdown (pages/markdown.go's
// renderMarkdown, wrapped in a ".krill-md" element at every call site) sane
// default spacing/typography -- the pinned Tailwind CDN build's preflight
// reset otherwise zeroes a <p>'s margin and a <ul>'s list-style, so
// goldmark's own <p>/<ul>/<pre>/<code>/<blockquote> output would render as
// an unstyled, list-marker-less wall of text. Mirrors whagent_net/ui's
// chatMarkdownCSS (same gap, same fix), scoped to ".krill-md" instead of
// ".chat-markdown" since krill's markdown is full-width page prose, not a
// chat bubble.
const markdownCSS = `
.krill-md :where(p) { margin: 0 0 0.5em; }
.krill-md :where(p):last-child { margin-bottom: 0; }
.krill-md :where(ul, ol) { margin: 0 0 0.5em 1.25em; }
.krill-md :where(ul) { list-style-type: disc; }
.krill-md :where(ol) { list-style-type: decimal; }
.krill-md :where(pre) { background: rgba(0, 0, 0, 0.15); padding: 0.5em 0.75em; border-radius: 0.5em; overflow-x: auto; margin: 0.5em 0; }
.krill-md :where(code) { font-family: ui-monospace, monospace; font-size: 0.875em; }
.krill-md :where(pre code) { background: none; padding: 0; }
.krill-md :where(code):not(pre code) { background: rgba(0, 0, 0, 0.15); padding: 0.1em 0.35em; border-radius: 0.3em; }
.krill-md :where(blockquote) { border-left: 3px solid currentColor; opacity: 0.8; padding-left: 0.75em; margin: 0.5em 0; }
.krill-md :where(a) { text-decoration: underline; }
.krill-md :where(h1, h2, h3, h4, h5, h6) { font-weight: 700; margin: 0.5em 0 0.25em; }
`

// relativeAgeScript upgrades every [data-krill-updated-at] element's text to
// "Updated N ago", read off the absolute RFC3339 instant the server put in
// the attribute (NFR 7b497d92).
//
// It lives in the document head, never in a fragment: the regions htmx swaps
// carry the instant and nothing else, so a relative string rendered by the
// server would be as old as the response and there would be nothing inside
// the region to say so. Deriving it here also means one implementation for
// every page rather than one per view, and it re-runs after each swap so a
// Refresh's new instant is picked up without a reload.
//
// It degrades to the instant the server rendered, which is why that text is
// the element's server-side content rather than an empty node: with
// JavaScript off the operator still sees when the page was read.
//
// Both listeners are on document, never on document.body: this script is
// emitted into the head, where <body> does not exist yet, so a
// document.body guard would evaluate false and bind nothing -- the
// swap upgrade would then never fire, and every in-place swap on the
// Tasks region (a filter change, a Refresh) would leave the element
// showing the absolute instant the server rendered.
//
// The swap listener is named `htmx:after:swap`, the event htmx 4.0.0
// dispatches; the 1.x camelCase name is never dispatched and would bind a
// listener that never fires.
const relativeAgeScript = `
(function(){
function ago(then){
var s=Math.max(0,Math.round((Date.now()-then)/1000));
if(s<60){return s+' second'+(s===1?'':'s');}
var m=Math.round(s/60); if(m<60){return m+' minute'+(m===1?'':'s');}
var h=Math.round(m/60); if(h<24){return h+' hour'+(h===1?'':'s');}
var d=Math.round(h/24); return d+' day'+(d===1?'':'s');
}
function upgrade(root){
var nodes=(root||document).querySelectorAll('[data-krill-updated-at]');
for(var i=0;i<nodes.length;i++){
var t=Date.parse(nodes[i].getAttribute('data-krill-updated-at'));
if(!isNaN(t)){nodes[i].textContent='Updated '+ago(t)+' ago';}
}
}
document.addEventListener('DOMContentLoaded',function(){upgrade(document);});
document.addEventListener('htmx:after:swap',function(e){upgrade(e.target);});
})();`

// copyTaskIdScript gives the task detail rail's Task id chip its behaviour:
// click copies the id to the clipboard and confirms in place. It is the one
// control on the page that cannot be server-rendered, for the reason
// NFR 7b497d92 names -- the rail is a fragment the Refresh button and every
// tab swap re-request, so state held only inside it would die with the swap.
// Deriving it in the head, outside every fragment, is also what lets it
// survive a tab swap without the server re-binding it.
//
// The page is read-only: this writes to the operator's own clipboard and
// POSTs nothing to krill, so there is no form, no hx-post and no operator
// route anywhere in the path.
//
// Degradation is deliberate on both axes. The chip renders disabled with the
// reason in its title, and this script is what enables it -- a control that
// is guaranteed to fail must not look live (design-htmx-ui, Page anatomy),
// because an operator who clicks a button that does nothing has been told
// the id is gone when it is not. And navigator.clipboard is absent on an
// insecure origin and refused under a denied clipboard permission; either
// way the failure selects the id instead, so the operator's next Ctrl+C
// works. Silence is the one outcome that is not acceptable here.
//
// Both listeners hang off `document`, never `document.body`, for the same
// reason as leaseCountdownScript: this is emitted from CustomHead, so a
// classic inline script here runs while the parser is still inside <head>
// and document.body is still null. htmx events bubble, so document sees
// every swap regardless.
const copyTaskIdScript = `
(function(){
var RESET_MS=2500;
var TONE={copied:'text-success',failed:'text-error'};
var BASE='ml-2 text-xs ';
function statusOf(btn){
var cell=btn.closest('dd');
return cell?cell.querySelector('[data-krill="copy-task-id-status"]'):null;
}
function announce(btn,state,msg){
var s=statusOf(btn);if(!s){return;}
if(btn._copyTimer){clearTimeout(btn._copyTimer);}
s.className=BASE+TONE[state];
s.textContent=msg;
btn.setAttribute('data-copy-state',state);
btn._copyTimer=setTimeout(function(){
if(!btn.isConnected){return;}
s.textContent='';btn.removeAttribute('data-copy-state');
},RESET_MS);
}
// Selecting the chip's own text is the failure path's real fallback: the id
// is readable either way, so selecting it makes the operator's next Ctrl+C
// succeed without the clipboard API. Best effort -- a browser that refuses
// the Range too still gets the message naming the manual step.
function selectId(btn){
try{
var sel=window.getSelection();if(!sel){return;}
var r=document.createRange();r.selectNodeContents(btn);
sel.removeAllRanges();sel.addRange(r);
}catch(e){}
}
function writeId(btn){
var id=btn.getAttribute('data-task-id')||(btn.textContent||'').trim();
var clip=(typeof navigator!=='undefined')?navigator.clipboard:null;
if(!clip||typeof clip.writeText!=='function'){return null;}
try{return clip.writeText(id);}catch(e){return null;}
}
function bind(btn){
// Bind once. The upgrade runs after every swap, and a chip two nested
// swapped fragments both contain would otherwise collect a second listener
// and announce twice per click.
if(btn.getAttribute('data-krill-bound')==='1'){return;}
btn.setAttribute('data-krill-bound','1');
// The upgrade the disabled chip was waiting for: it works now, and its
// title says so instead of still blaming missing JavaScript.
btn.removeAttribute('disabled');
btn.setAttribute('title','Copy the task id to your clipboard');
btn.addEventListener('click',function(ev){
ev.preventDefault();
var p=writeId(btn);
if(!p||typeof p.then!=='function'){
selectId(btn);
announce(btn,'failed','Could not copy. The id is selected - press Ctrl+C.');
return;
}
p.then(function(){
announce(btn,'copied','Copied');
},function(){
selectId(btn);
announce(btn,'failed','Could not copy. The id is selected - press Ctrl+C.');
});
});
}
function upgrade(root){
var nodes=(root||document).querySelectorAll('[data-krill="copy-task-id"]');
for(var i=0;i<nodes.length;i++){bind(nodes[i]);}
}
document.addEventListener('DOMContentLoaded',function(){upgrade(document);});
document.addEventListener('htmx:after:swap',function(e){upgrade(e.target);});
})();`

// renderShell writes one signed-in page: the workspace chrome plus body,
// at HTTP 200.
//
// body is a templ.Component rather than pre-rendered HTML: templ has no
// "content" concept, so the caller composes the page and the seam owns
// the document around it. Every app route is mounted behind
// app.auth.RequireAuthFunc by mountShellRoutes, so the identity read here
// is always present.
//
// activePath is the path the nav marks from -- the page's own URL, or the
// nav key for a page whose URL is not one of the nav's own (the shell home
// renders the Overview, whose nav key is the product's overview URL).
func (app *App) renderShell(w http.ResponseWriter, r *http.Request, title, activePath string, body templ.Component) {
	app.renderShellStatus(w, r, title, activePath, body, http.StatusOK)
}

// renderShellStatus is renderShell with an explicit status code, so a
// page that renders a real 404/400/500 body still does so inside the
// chrome rather than as a bare http.Error string.
//
// The status necessarily lives here rather than in a component: templ
// components are body-writers with no status concept, which is exactly
// the capability the old renderShellStatus already provided. spec_page.go
// and interventions.go depend on it.
//
// The component is rendered into a buffer *before* WriteHeader, so a
// component that fails to render leaves the response unwritten rather
// than committing a status and then truncating the body.
func (app *App) renderShellStatus(w http.ResponseWriter, r *http.Request, title, activePath string, body templ.Component, status int) {
	r = app.withShellProduct(w, r)

	// A plain string, never an htmxauth.UserInfo: htmxui §1 requires
	// components stay free of a specific auth dependency, so no auth type
	// may reach one.
	var userLabel string
	if u := htmxauth.GetUser(r.Context()); u != nil {
		userLabel = u.PreferredUsername
	}

	productID, _ := currentProduct(r.Context())

	page := shellWithBody(
		workspaceShellData(
			app.shellNavTargets(r.Context(), productID.ID),
			activePath, title, userLabel,
			// The switcher is read here, by the one seam every page goes
			// through, rather than left to each route. Passing nil instead
			// would compile and ship a sidebar with no Product select --
			// the operator loses the one control that carries the product
			// in the URL, with nothing failing.
			app.productSwitcherData(r),
		),
		withFlashSuccess(r, w, body))

	var buf bytes.Buffer
	if err := page.Render(r.Context(), &buf); err != nil {
		// Every component is compiled by templ and every value passed
		// here is this package's own, so this is a programming mistake,
		// not a runtime condition.
		panic(err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := htmxbase.Render(w, htmxbase.LayoutData{
		Title:       title,
		TitleSuffix: "krill",
		Content:     template.HTML(buf.String()), //nolint:gosec // this package's own rendered component
		CustomHead:  template.HTML(buildHead()),  //nolint:gosec // fixed pinned CDN markup plus the embedded stylesheet
	}); err != nil {
		panic(err)
	}
}

// withShellProduct guarantees the request carries a current product before
// the chrome is assembled, so every shell page's sidebar names the product
// that page is about. A resolver's own answer wins; a URL that names its
// product in the path supplies it directly; an un-prefixed page resolves
// one server-side and records it as the last-viewed.
func (app *App) withShellProduct(w http.ResponseWriter, r *http.Request) *http.Request {
	if _, ok := currentProduct(r.Context()); ok {
		return r
	}
	if pid, _ := shellPathTargets(r.URL.Path); pid != uuid.Nil {
		// The id is the URL's own and is checked against the caller's
		// scope by the handler that serves it; the chrome only needs it to
		// build its own hrefs.
		return withCurrentProduct(r, store.Product{ID: pid})
	}
	r, _ = app.rememberUnprefixedProduct(w, r)
	return r
}

// shellPathTargets is the product, and the milestone or milepebble under
// it, that a product-scoped URL names: the id under /products/{id}/...,
// /spec/products/{id}/... or /design/products/{id}/..., and the container
// id under that product's /milestones/ prefix.
//
// Both are uuid.Nil for a URL that names neither, which is the un-prefixed
// case the resolvers answer.
//
// The container id is no longer read by the chrome: Tasks and Board link
// at the product-wide pages, so no nav href depends on which milestone a
// page happens to be scoped to. It is still parsed, and still covered by
// overview_page_test.go, because the function's shape is a path reader
// rather than a nav helper -- narrowing it to one return would leave the
// next path-derived id with nowhere to go.
func shellPathTargets(path string) (product, milestone uuid.UUID) {
	segments := pathSegments(path)
	// The id sits directly after "products", which is itself at the root
	// or under /spec or /design; the container sits directly after
	// "milestones".
	const productsSegment, milestonesSegment = "products", "milestones"
	switch {
	case len(segments) >= 2 && segments[0] == productsSegment:
		product = parseUUID(segments[1])
	case len(segments) >= 3 && segments[1] == productsSegment &&
		(segments[0] == strings.TrimPrefix(specPath, "/") || segments[0] == strings.TrimPrefix(designPath, "/")):
		product = parseUUID(segments[2])
	default:
		return uuid.Nil, uuid.Nil
	}
	if product == uuid.Nil {
		return uuid.Nil, uuid.Nil
	}
	for i, segment := range segments {
		if segment == milestonesSegment && i+1 < len(segments) {
			return product, parseUUID(segments[i+1])
		}
	}
	return product, uuid.Nil
}

// parseUUID is uuid.Parse for a path segment, answering Nil for anything
// that is not a UUID -- which is how an id-shaped segment that is really
// some other word never reaches a store call.
func parseUUID(segment string) uuid.UUID {
	id, err := uuid.Parse(segment)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// renderFragment writes a bare component at HTTP 200, with no chrome and
// no document layout -- the HX-Request half of a one-route-two-modes
// branch.
//
// The status is always 200: an htmx swap target's HTTP status is not
// surfaced to the operator, so a failure has to ride inside the fragment
// rather than in a status code. See the agent guide's error rule.
//
// The component is buffered before the status is committed, for the same
// reason renderShellStatus buffers: a component that fails partway
// through would otherwise leave a truncated fragment swapped into the
// page at 200, silently corrupting it. Once a byte is on the wire there is
// no way to take it back, so the render has to succeed first.
func renderFragment(w http.ResponseWriter, r *http.Request, c templ.Component) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		// The component is compiled by templ and every value is this
		// package's own, so a failure here is a programming mistake, not
		// a runtime condition -- the same reasoning as the page path.
		panic(err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		logger.Error("failed to write fragment", "error", err)
	}
}

// shellWithBody composes the chrome around a page body. templ passes a
// component's children through the context rather than as a parameter, so
// calling components.Shell from Go means re-attaching the body to the
// context inside a ComponentFunc.
func shellWithBody(data components.ShellData, body templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return components.Shell(data).Render(templ.WithChildren(ctx, body), w)
	})
}

// mustRenderComponent renders c to a string, panicking on failure.
//
// Production goes through renderShell/renderFragment. Tests use this: the
// question they ask is "does this data reach the page", so a plain string
// is the right shape and keeps ~40 call sites free of error plumbing.
// Every component is compiled by templ and every value is this package's
// own, so failure here is a programming mistake, not a runtime condition.
func mustRenderComponent(c templ.Component) string {
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		panic(err)
	}
	return buf.String()
}
