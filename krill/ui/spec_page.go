// The spec browser: a product's capability map, its load-bearing
// decisions, its personas, and its non-goals, each rendered inside the
// shell chrome. Every page is scoped to one product at /spec/products/{id}
// and reads through app.spec (readclient.go) -- the same //krill/slice
// Querier and //krill/store readers the MCP tools get_product_slice /
// list_personas / list_non_goals call underneath, so a page and the
// matching tool always show the same current spec.
//
// All four pages render current revisions only: the reader's GetCurrent /
// ListCurrentByProduct methods never touch history, so a superseded
// revision is never surfaced.
package main

import (
	"errors"
	"html/template"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// specProductsPath lists the products an operator can browse into;
// specProductPath is the prefix every per-product page hangs off. The
// delivery/roadmap view a sibling task adds under /spec does not collide
// with these, which all live under the /spec/products/{id} prefix.
const (
	specProductsPath = specPath + "/products"
	specProductPath  = specPath + "/products/{id}"
)

// specProductID validates the {id} path value as a product id, writing a
// shell-rendered 400 and returning ok=false when it is not a UUID.
func specProductID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		renderSpecStatus(w, r, "Spec", specPath, http.StatusBadRequest,
			"<h2>Bad product id</h2><p>The product id in the URL is not a UUID.</p>")
		return uuid.Nil, false
	}
	return id, true
}

// renderSpecError maps a spec read error to a page. store.ErrNotFound
// (an unknown or superseded product) is a 404 the operator can act on;
// anything else is a genuine read failure and is logged at ERROR before a
// 500. Both render inside the shell, not as a bare http.Error string.
func renderSpecError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		renderSpecStatus(w, r, "Spec", specPath, http.StatusNotFound,
			"<h2>Not found</h2><p>No current product matches that id.</p>")
		return
	}
	logger.Error("spec read failed", "error", err)
	renderSpecStatus(w, r, "Spec", specPath, http.StatusInternalServerError,
		"<h2>Could not load the spec</h2><p>The spec store could not be read. See the logs.</p>")
}

// renderSpecStatus renders a bare, already-safe page body through the
// shell with an explicit status, for the error and not-yet-wired cases
// that have no template of their own.
func renderSpecStatus(w http.ResponseWriter, r *http.Request, title, activePath string, status int, body template.HTML) {
	renderShellStatus(w, r, title, activePath, body, status)
}

// productHeader is the common "which product am I looking at" banner the
// per-product pages show.
type productHeader struct {
	Name   string
	Vision string
	Href   string // the product's capability-map page
}

// productHeaderOf builds the banner from a store product's own current
// row and the {id} it is browsed at.
func productHeaderOf(p store.Product) productHeader {
	return productHeader{Name: p.Name, Vision: p.Vision, Href: productPath(p.ID)}
}

// productHeaderOfEntity builds the same banner from a slice.Document's
// optional Product entity, for the pages that already have the document
// (the capability map and decisions) and do not need a second product
// read. A nil Product -- which get_product_slice does not produce on
// success, but the shared type allows -- yields a name-less banner rather
// than a panic.
func productHeaderOfEntity(p *slice.ProductEntity, id uuid.UUID) productHeader {
	if p == nil {
		return productHeader{Href: productPath(id)}
	}
	return productHeader{Name: p.Name, Vision: p.Vision, Href: productPath(id)}
}

// productPath is the capability-map page for a product.
func productPath(id uuid.UUID) string {
	return specPath + "/products/" + id.String()
}

// -- product index --------------------------------------------------------

// productListItem is one row of the product index.
type productListItem struct {
	Name        string
	Vision      string
	CapabilityH string // /spec/products/{id} -- the capability map
}

var productsTemplate = template.Must(template.New("products").Parse(`<h2>Products</h2>
{{if .}}
<ul>
{{range .}}<li><a href="{{.CapabilityH}}">{{.Name}}</a>{{if .Vision}} &mdash; {{.Vision}}{{end}}</li>
{{end}}</ul>
{{else}}
<p>No products yet.</p>
{{end}}`))

// handleSpecProducts lists the current products in the deployment's sole
// scope so an operator can pick one to browse. It mirrors the mcp
// list_products discovery shape, which also resolves the sole scope rather
// than asking the caller to name one.
func (app *App) handleSpecProducts(w http.ResponseWriter, r *http.Request) {
	products, err := app.spec.Products(r.Context())
	if err != nil {
		renderSpecError(w, r, err)
		return
	}

	items := make([]productListItem, 0, len(products))
	for _, p := range products {
		items = append(items, productListItem{
			Name:        p.Name,
			Vision:      p.Vision,
			CapabilityH: productPath(p.ID),
		})
	}
	renderShell(w, r, "Products", specPath, renderPage(productsTemplate, items))
}

// -- capability map -------------------------------------------------------

// capabilityFeatureSet is one FeatureSet and the Features nested under it.
type capabilityFeatureSet struct {
	Name        string
	Description string
	Features    []capabilityFeature
}

// capabilityFeature is one Feature and the Requirements nested under it.
type capabilityFeature struct {
	Number       int // Cn -- the stored DisplayNumber, the number a reader cites
	Name         string
	Description  string
	Requirements []capabilityRequirement
}

// capabilityRequirement is one FR or NFR under a Feature.
type capabilityRequirement struct {
	Kind string // "FR" or "NFR"
	Name string
	Body string
}

type capabilityPage struct {
	Product     productHeader
	FeatureSets []capabilityFeatureSet
}

var capabilityTemplate = template.Must(template.New("capability").Parse(`<h2>{{.Product.Name}}</h2>
{{if .Product.Vision}}<p>{{.Product.Vision}}</p>{{end}}
{{if .FeatureSets}}
{{range .FeatureSets}}
<h3>{{.Name}}</h3>
{{if .Description}}<p>{{.Description}}</p>{{end}}
{{if .Features}}
<ul>
{{range .Features}}
  <li>
    <strong>C{{.Number}} &mdash; {{.Name}}</strong>
    {{if .Description}}<div>{{.Description}}</div>{{end}}
    {{if .Requirements}}
    <ul>
    {{range .Requirements}}
      <li><strong>{{.Kind}}</strong>: {{.Name}}{{if .Body}}<div>{{.Body}}</div>{{end}}</li>
    {{end}}
    </ul>
    {{end}}
  </li>
{{end}}
</ul>
{{end}}
{{end}}
{{else}}
<p>No feature sets yet.</p>
{{end}}`))

// handleCapabilityMap renders a product's FeatureSets, Features, and
// Requirements (current revisions only) as a navigable capability map --
// get_product_slice's shape, nested by parent. FR 638a7e5f.
func (app *App) handleCapabilityMap(w http.ResponseWriter, r *http.Request) {
	productID, ok := specProductID(w, r)
	if !ok {
		return
	}

	doc, err := app.spec.ProductSlice(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}

	page := capabilityPage{Product: productHeaderOfEntity(doc.Product, productID)}

	// Index requirements and features by parent id so the template can
	// nest them without a second pass per level.
	reqsByFeature := map[uuid.UUID][]capabilityRequirement{}
	for _, rq := range doc.Requirements {
		reqsByFeature[rq.FeatureID] = append(reqsByFeature[rq.FeatureID], capabilityRequirement{
			Kind: rq.Kind,
			Name: rq.Name,
			Body: deref(rq.Body),
		})
	}
	featsBySet := map[uuid.UUID][]capabilityFeature{}
	for _, f := range doc.Features {
		featsBySet[f.FeatureSetID] = append(featsBySet[f.FeatureSetID], capabilityFeature{
			Number:       f.DisplayNumber,
			Name:         f.Name,
			Description:  deref(f.Description),
			Requirements: reqsByFeature[f.ID],
		})
	}
	for _, fs := range doc.FeatureSets {
		page.FeatureSets = append(page.FeatureSets, capabilityFeatureSet{
			Name:        fs.Name,
			Description: deref(fs.Description),
			Features:    featsBySet[fs.ID],
		})
	}

	renderShell(w, r, "Capability map", specPath, renderPage(capabilityTemplate, page))
}

// -- load-bearing decisions -----------------------------------------------

type decisionItem struct {
	Number int // LBn -- the stored DisplayNumber
	Name   string
	Body   string
}

type decisionsPage struct {
	Product   productHeader
	Decisions []decisionItem
}

var decisionsTemplate = template.Must(template.New("decisions").Parse(`<h2>{{.Product.Name}} &mdash; load-bearing decisions</h2>
{{if .Product.Vision}}<p>{{.Product.Vision}}</p>{{end}}
{{if .Decisions}}
<ol>
{{range .Decisions}}
  <li><strong>LB{{.Number}} &mdash; {{.Name}}</strong>{{if .Body}}<div>{{.Body}}</div>{{end}}</li>
{{end}}
</ol>
{{else}}
<p>No load-bearing decisions yet.</p>
{{end}}`))

// handleSpecDecisions lists a product's current LoadBearingDecisions with
// their full body text -- get_product_slice's decisions, unchanged (FR
// 6aa70e3a).
func (app *App) handleSpecDecisions(w http.ResponseWriter, r *http.Request) {
	productID, ok := specProductID(w, r)
	if !ok {
		return
	}

	doc, err := app.spec.ProductSlice(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}

	page := decisionsPage{Product: productHeaderOfEntity(doc.Product, productID)}
	for _, d := range doc.Decisions {
		page.Decisions = append(page.Decisions, decisionItem{
			Number: d.DisplayNumber,
			Name:   d.Name,
			Body:   deref(d.Body),
		})
	}

	renderShell(w, r, "Decisions", specPath, renderPage(decisionsTemplate, page))
}

// -- personas -------------------------------------------------------------

type personaItem struct {
	Name        string
	Description string
}

type personasPage struct {
	Product  productHeader
	Personas []personaItem
}

var personasTemplate = template.Must(template.New("personas").Parse(`<h2>{{.Product.Name}} &mdash; personas</h2>
{{if .Product.Vision}}<p>{{.Product.Vision}}</p>{{end}}
{{if .Personas}}
<ul>
{{range .Personas}}
  <li><strong>{{.Name}}</strong>{{if .Description}}<div>{{.Description}}</div>{{end}}</li>
{{end}}
</ul>
{{else}}
<p>No personas yet.</p>
{{end}}`))

// handleSpecPersonas lists a product's current Personas -- list_personas'
// shape (FR b4c1c77f).
func (app *App) handleSpecPersonas(w http.ResponseWriter, r *http.Request) {
	productID, ok := specProductID(w, r)
	if !ok {
		return
	}

	product, err := app.spec.Product(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}
	personas, err := app.spec.Personas(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}

	page := personasPage{Product: productHeaderOf(product)}
	for _, p := range personas {
		page.Personas = append(page.Personas, personaItem{
			Name:        p.Name,
			Description: deref(p.Description),
		})
	}

	renderShell(w, r, "Personas", specPath, renderPage(personasTemplate, page))
}

// -- non-goals ------------------------------------------------------------

type nonGoalItem struct {
	Kind string // "permanent" or "deferred"
	Name string
	Body string
}

type nonGoalsPage struct {
	Product  productHeader
	NonGoals []nonGoalItem
}

var nonGoalsTemplate = template.Must(template.New("non_goals").Parse(`<h2>{{.Product.Name}} &mdash; non-goals</h2>
{{if .Product.Vision}}<p>{{.Product.Vision}}</p>{{end}}
{{if .NonGoals}}
<ul>
{{range .NonGoals}}
  <li><strong>{{.Kind}}</strong>: {{.Name}}{{if .Body}}<div>{{.Body}}</div>{{end}}</li>
{{end}}
</ul>
{{else}}
<p>No non-goals yet.</p>
{{end}}`))

// handleSpecNonGoals lists a product's current Non-Goals, both the
// permanent and deferred kinds -- list_non_goals' shape (FR b4c1c77f).
func (app *App) handleSpecNonGoals(w http.ResponseWriter, r *http.Request) {
	productID, ok := specProductID(w, r)
	if !ok {
		return
	}

	product, err := app.spec.Product(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}
	nonGoals, err := app.spec.NonGoals(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}

	page := nonGoalsPage{Product: productHeaderOf(product)}
	for _, n := range nonGoals {
		page.NonGoals = append(page.NonGoals, nonGoalItem{
			Kind: string(n.Kind),
			Name: n.Name,
			Body: deref(n.Body),
		})
	}

	renderShell(w, r, "Non-goals", specPath, renderPage(nonGoalsTemplate, page))
}

// -- helpers --------------------------------------------------------------

// deref unwraps an optional text field, treating absent as empty so a
// template never has to nil-check a body or description.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
