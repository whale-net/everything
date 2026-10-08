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

// buildHead is krill's htmxbase CustomHead: theme bootstrap, Tailwind, daisyUI,
// then ThemesCSS. ThemesCSS must follow daisyUI or it silently loses, and
// htmxbase renders CustomCSS first, so it belongs here.
func buildHead() string {
	return fmt.Sprintf(`<script>
(function(){var KEY=%q;var t=null;try{t=localStorage.getItem(KEY);}catch(e){t=null;}
if(!t){t=window.matchMedia('(prefers-color-scheme: dark)').matches?'night':'light';}
document.documentElement.setAttribute('data-theme',t);})();
</script>
<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4.3.3/dist/index.global.js"></script>
<style type="text/tailwindcss">
</style>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui@5.6.18/daisyui.css">
<style>%s</style>
<style>%s</style>
<script>%s</script>
<script>%s</script>
<script>%s</script>`, htmxui.ThemeSwitcherStorageKey, htmxui.ThemesCSS, markdownCSS+shellCSS, relativeAgeScript, leaseCountdownScript, copyTaskIdScript)
}

// leaseCountdownScript rewrites each lease <time> to "Lease in N min" or
// "Lease expired N min ago" from its datetime, so swapped fragments never
// carry stale relative text. Listeners sit on document (body is null in <head>).
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

// markdownCSS restores spacing and list markers for ".krill-md" prose, which
// Tailwind's preflight reset otherwise strips.
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

// shellCSS: krill's header is base-100, so its ghost navbar buttons need
// base-content rather than themes.css's neutral-content.
const shellCSS = `
[data-krill="workspace-shell"] .navbar .btn-ghost { color: var(--color-base-content); }
`

// relativeAgeScript rewrites [data-krill-updated-at] text to "N ago" from the
// attribute, re-running after each swap; freshness stamps read "Updated N ago".
// Listeners sit on document because <body> does not exist yet in <head>.
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
if(isNaN(t)){continue;}
var hook=nodes[i].getAttribute('data-krill')||'';
nodes[i].textContent=(/updated-at$/.test(hook)?'Updated ':'')+ago(t)+' ago';
}
}
document.addEventListener('DOMContentLoaded',function(){upgrade(document);});
document.addEventListener('htmx:after:swap',function(e){upgrade(e.target);});
})();`

// copyTaskIdScript enables the task id chip (rendered disabled) and copies
// the id on click. When the clipboard API is unavailable it selects the id
// instead. Lives in the head so it survives fragment swaps.
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
// What this control copies. A chip normally holds the value itself, in
// data-task-id or as its own text; a control carrying data-copy-source
// reads the field it names instead, so a one-time secret appears once in
// the page rather than twice (once in the field, once in the control).
function valueOf(btn){
var src=btn.getAttribute('data-copy-source');
if(src){var el=document.getElementById(src);if(el&&typeof el.value==='string'){return el.value;}}
return btn.getAttribute('data-task-id')||(btn.textContent||'').trim();
}
// Selecting the value is the failure path's real fallback: it is readable
// either way, so selecting it makes the operator's next Ctrl+C succeed
// without the clipboard API. Best effort -- a browser that refuses the
// Range too still gets the message naming the manual step.
function selectId(btn){
try{
var src=btn.getAttribute('data-copy-source');
var el=src?document.getElementById(src):null;
if(el&&typeof el.select==='function'){el.select();return;}
var sel=window.getSelection();if(!sel){return;}
var r=document.createRange();r.selectNodeContents(btn);
sel.removeAllRanges();sel.addRange(r);
}catch(e){}
}
function writeId(btn){
var id=valueOf(btn);
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
// title says so instead of still blaming missing JavaScript. The noun is
// the control's to name: a token is not a task id.
btn.removeAttribute('disabled');
btn.setAttribute('title','Copy the '+(btn.getAttribute('data-copy-label')||'task id')+' to your clipboard');
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

// renderShell writes one signed-in page (chrome plus body) at 200.
// activePath is the nav key to mark, usually the page's own URL.
func (app *App) renderShell(w http.ResponseWriter, r *http.Request, title, activePath string, body templ.Component) {
	app.renderShellStatus(w, r, title, activePath, body, http.StatusOK)
}

// renderShellStatus is renderShell with an explicit status. The body is
// buffered before WriteHeader so a render failure never truncates a
// committed response.
func (app *App) renderShellStatus(w http.ResponseWriter, r *http.Request, title, activePath string, body templ.Component, status int) {
	r = app.withShellProduct(w, r)

	// A plain string: components must not depend on an auth type.
	var userLabel string
	if u := htmxauth.GetUser(r.Context()); u != nil {
		userLabel = u.PreferredUsername
	}

	productID, _ := currentProduct(r.Context())

	page := shellWithBody(
		workspaceShellData(
			app.shellNavTargets(r.Context(), productID.ID),
			activePath, title, userLabel,
			// Passing nil here would silently drop the sidebar's Product select.
			app.productSwitcherData(r),
		),
		withFlashSuccess(r, w, body))

	var buf bytes.Buffer
	if err := page.Render(r.Context(), &buf); err != nil {
		// Components are compiled and values are ours, so failure is a bug.
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

// hxTargetID returns the id htmx is swapping. htmx 4 sends HX-Target as
// "tag#id"; older versions sent the bare id, so both resolve.
func hxTargetID(r *http.Request) string {
	target := r.Header.Get("HX-Target")
	if i := strings.LastIndex(target, "#"); i >= 0 {
		return target[i+1:]
	}
	return target
}

// withShellProduct ensures the request carries a current product before the
// chrome is built: a resolver's answer, then the URL's product, then a
// server-side pick recorded as last-viewed.
func (app *App) withShellProduct(w http.ResponseWriter, r *http.Request) *http.Request {
	if _, ok := currentProduct(r.Context()); ok {
		return r
	}
	if pid, _ := shellPathTargets(r.URL.Path); pid != uuid.Nil {
		// The handler checks the id's scope; the chrome only builds hrefs from it.
		return withCurrentProduct(r, store.Product{ID: pid})
	}
	r, _ = app.rememberUnprefixedProduct(w, r)
	return r
}

// shellPathTargets returns the product id and milestone container id a
// product-scoped URL names, or uuid.Nil for each it does not.
func shellPathTargets(path string) (product, milestone uuid.UUID) {
	segments := pathSegments(path)
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

// parseUUID is uuid.Parse for a path segment, returning Nil for non-UUIDs.
func parseUUID(segment string) uuid.UUID {
	id, err := uuid.Parse(segment)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// renderFragment writes a bare component at 200 for htmx requests; errors
// must ride inside the fragment since htmx hides the status. Buffered so a
// failed render never swaps in a truncated fragment.
func renderFragment(w http.ResponseWriter, r *http.Request, c templ.Component) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		// Components are compiled and values are ours, so failure is a bug.
		panic(err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		logger.Error("failed to write fragment", "error", err)
	}
}

// shellWithBody wraps body in the chrome. templ passes children via context,
// so the body is re-attached inside a ComponentFunc.
func shellWithBody(data components.ShellData, body templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return components.Shell(data).Render(templ.WithChildren(ctx, body), w)
	})
}

// mustRenderComponent renders c to a string, panicking on failure. Used by
// tests.
func mustRenderComponent(c templ.Component) string {
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		panic(err)
	}
	return buf.String()
}
