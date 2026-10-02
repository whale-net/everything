// The product-wide task read layer the Tasks table and the Board share:
// one scope type parsed from the query string, one resolver that checks a
// selected container belongs to the product its URL names, and one read
// that turns a resolved scope into a page of rows plus the total behind it
// (FR 7191dba1, 61d7fb7b).
//
// It is deliberately the read layer and nothing more: no controls, no rows,
// no board columns. The handlers built on it serve an honest region the
// later pages fill in, so this file is where the scope's own rules live
// rather than being re-derived per handler.
package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// The query parameters the Tasks and Board URLs carry. They are the same
// names krill api's GET /products/{id}/tasks reads (handlers/
// task_product_list.go), so one filter set has one spelling everywhere: the
// link an operator copies is the request the api answers.
const (
	productTaskScopeParam     = pages.ProductTaskScopeQueryParam
	productTaskContainerParam = pages.ProductTaskContainerQueryParam
	productTaskLaneParam      = pages.ProductTaskLaneQueryParam
	productTaskOnlyStuckParam = pages.ProductTaskOnlyStuckQueryParam
	productTaskPageSizeParam  = "page_size"
	productTaskPageTokenParam = "page_token"

	// productTaskMilestoneParam names the PARENT milestone whose milepebbles
	// the milepebble select offers. It is a UI-only parameter: it never
	// reaches the store, because the read's container is still the
	// milepebble. It exists because a milepebble id alone cannot say which
	// options the select should offer -- the operator changes the milestone
	// select to change that offer, before any milepebble is chosen -- so the
	// URL has to carry the milestone too.
	productTaskMilestoneParam = pages.ProductTaskMilestoneQueryParam
)

// productTaskScope is the Tasks/Board scope control's state, parsed from
// one request's query string and nowhere else.
//
// Kind mirrors store.ProductTaskScopeKind's three values by name rather
// than re-inventing a UI vocabulary: an absent scope is the store's own
// product-wide default, store.ProductTaskScopeIncomplete, whose membership
// is judged per container by store.IsIncompleteContainerStatus. The two
// single-container kinds carry a ContainerID, resolved against the
// product's own containers before the read -- never an id the URL merely
// spelled.
type productTaskScope struct {
	// Kind is one of the store's three scope kinds. The zero value is not
	// meaningful: parseProductTaskScope always fills it.
	Kind store.ProductTaskScopeKind

	// ContainerID is the milestone or milepebble the two single-container
	// kinds name. uuid.Nil means the operator has not chosen one, which the
	// resolver answers from the product's own containers rather than by
	// refusing the request.
	ContainerID uuid.UUID

	// MilestoneID is the parent milestone the milepebble select offers, read
	// from productTaskMilestoneParam. It is meaningful only in milepebble
	// mode and never reaches the store; uuid.Nil means the URL did not say,
	// and the resolver then answers it from the selected milepebble's own
	// parent. Under the other two modes it is inert, like ContainerID is
	// under the product-wide one.
	MilestoneID uuid.UUID

	// Lane is the optional lane filter. nil is every lane, never "no lanes".
	Lane *store.Lane

	OnlyStuck bool

	// PageSize and PageToken are the raw paging pair as the URL spelled
	// them. PageSize is resolved by the store (ResolvePageSize), not here,
	// so this layer and the api apply the same clamp.
	PageSize  int
	PageToken string
}

// productTaskScopeProblem is the named outcome a scope can fail with,
// which the handlers turn into a status rather than each guessing which
// error means what.
//
// It is a closed set because each member has a distinct answer for the
// operator: a value the URL got wrong (400), a container that is not this
// product's (404 -- never another product's rows, never a plausible empty
// page), a container selection this product has nothing to answer (an
// ordinary empty result), and a read that failed (500, or a 200 with the
// failure inline when htmx asked).
type productTaskScopeProblem int

const (
	// productTaskScopeOK is the absence of a problem.
	productTaskScopeOK productTaskScopeProblem = iota
	// productTaskScopeInvalid is a malformed scope, container id, lane,
	// only_stuck value or page size in the query string.
	productTaskScopeInvalid
	// productTaskScopeNotFound is a selected milestone or milepebble that
	// does not belong to the product the URL names.
	productTaskScopeNotFound
	// productTaskScopeNoContainers is a single-container scope over a
	// product that has no container of that kind to select.
	productTaskScopeNoContainers
	// productTaskScopeNoMilepebbles is milepebble mode pointed at a
	// milestone of a product that HAS milestones, none of them cut. It is
	// its own outcome because the control's milestone select is exactly
	// what would let the operator pick a cut one.
	productTaskScopeNoMilepebbles
	// productTaskScopeUnreadable is a delivery-listing read that failed.
	productTaskScopeUnreadable
)

func (p productTaskScopeProblem) String() string {
	switch p {
	case productTaskScopeInvalid:
		return "invalid scope"
	case productTaskScopeNotFound:
		return "no such milestone or milepebble in this product"
	case productTaskScopeNoContainers:
		return "this product has no milestone or milepebble to scope to"
	case productTaskScopeNoMilepebbles:
		return "that milestone has no milepebbles cut"
	case productTaskScopeUnreadable:
		return "the delivery listing could not be read"
	}
	return "ok"
}

// resolvedProductTaskScope is a parsed scope whose container has been
// checked against the product's own delivery listing, so the store read it
// is built from names a container this product actually owns.
type resolvedProductTaskScope struct {
	// Parsed is the scope as the URL spelled it, kept for the region's own
	// controls to read back.
	Parsed productTaskScope

	// Store is the store.ProductTaskScope to read with. It equals Parsed's
	// kind, with ContainerID set to the id the resolver settled on --
	// which differs from Parsed's only when the operator named a mode with
	// no id and the resolver picked one.
	Store store.ProductTaskScope

	// Container is the resolved milestone or milepebble, with the name and
	// kind the page shows. Its zero value in the product-wide scope, which
	// names no container.
	Container taskContainer

	// Milestone is the milestone the control's milestone select marks
	// selected: the resolved container itself in milestone mode, and the
	// PARENT of the resolved milepebble in milepebble mode. Its zero value
	// in the product-wide scope.
	//
	// A milepebble's parent is what makes the milepebble mode's second
	// select possible at all. The selected milepebble alone does not say
	// which options that select should offer, so without this the mode
	// would render an empty list and nothing marked as chosen.
	Milestone taskContainer

	// Milestones is the product's own milestones, in the listing's order,
	// so the scope control can offer them as options and mark which one
	// this request resolved to. Empty in the product-wide scope, which
	// reads no listing.
	Milestones []taskContainer
}

// productTaskPage is one page of the product-wide read plus the total
// behind it -- the "Y" in "Showing X of Y tasks", read for the same
// filters as the rows rather than derived from them.
type productTaskPage struct {
	Rows      []store.ProductTaskRow
	Total     int
	NextToken string

	// PageToken is the token that produced this page, so a control can read
	// back where it is. Empty on the first page.
	PageToken string
}

// parseProductTaskScope reads the scope control's state off one request's
// query string.
//
// An absent scope is the store's own product-wide default,
// store.ProductTaskScopeIncomplete -- the same value the api's parser
// defaults to, so a bare URL means the same thing to both. An absent lane
// means every lane and an absent only_stuck means false; neither is a
// filter that excludes everything.
//
// Every rejection here is productTaskScopeInvalid: a value the URL spelled
// wrongly is the operator's to fix, and it is refused before any read.
func parseProductTaskScope(q url.Values) (productTaskScope, productTaskScopeProblem) {
	raw := q.Get(productTaskScopeParam)
	if raw == "" {
		raw = string(store.ProductTaskScopeIncomplete)
	}
	kind := store.ProductTaskScopeKind(raw)
	switch kind {
	case store.ProductTaskScopeIncomplete, store.ProductTaskScopeMilestone, store.ProductTaskScopeMilepebble:
	default:
		return productTaskScope{}, productTaskScopeInvalid
	}

	scope := productTaskScope{Kind: kind}

	if (store.ProductTaskScope{Kind: scope.Kind}).RequiresContainer() {
		// An unparseable id here is not refused: the mode names a kind of
		// container and the operator has simply not picked one yet, which
		// the resolver answers from the product's own containers. A
		// non-empty value that is not a UUID is a mistyped id, though, and
		// has to be said so rather than silently reading some other
		// container's tasks.
		if id := strings.TrimSpace(q.Get(productTaskContainerParam)); id != "" {
			parsed, err := uuid.Parse(id)
			if err != nil {
				return productTaskScope{}, productTaskScopeInvalid
			}
			scope.ContainerID = parsed
		}
		// The parent milestone is parsed the same way and refused the same
		// way: a non-empty value that is not a UUID is a mistyped id, not an
		// absent selection. Only milepebble mode reads it afterwards; in
		// milestone mode the milestone select submits container_id instead,
		// so a milestone here is simply unused.
		if raw := strings.TrimSpace(q.Get(productTaskMilestoneParam)); raw != "" {
			parsed, err := uuid.Parse(raw)
			if err != nil {
				return productTaskScope{}, productTaskScopeInvalid
			}
			scope.MilestoneID = parsed
		}
	}
	// A container id under the product-wide scope is left inert rather than
	// refused: the store's own contract says so ("Ignored for
	// ProductTaskScopeIncomplete, which names no container"), the api's
	// parser returns before it ever reads the parameter, and the MCP tool
	// is pinned to the same. Refusing it here would make one query string
	// mean a 400 in the console and a 200 everywhere else, and would break
	// the ordinary case of an operator switching back to the product-wide
	// mode from a link that still carries the id they had selected, which
	// the control cannot un-ask for them.

	if rawLane := q.Get(productTaskLaneParam); rawLane != "" {
		lane, ok := canonicalLaneOf(rawLane)
		if !ok {
			return productTaskScope{}, productTaskScopeInvalid
		}
		scope.Lane = &lane
	}

	rawStuck := q.Get(productTaskOnlyStuckParam)
	if rawStuck != "" {
		onlyStuck, err := strconv.ParseBool(rawStuck)
		if err != nil {
			return productTaskScope{}, productTaskScopeInvalid
		}
		scope.OnlyStuck = onlyStuck
	}

	rawSize := q.Get(productTaskPageSizeParam)
	if rawSize != "" {
		size, err := strconv.Atoi(rawSize)
		if err != nil || size < 0 {
			return productTaskScope{}, productTaskScopeInvalid
		}
		scope.PageSize = size
	}

	scope.PageToken = q.Get(productTaskPageTokenParam)
	return scope, productTaskScopeOK
}

// canonicalLaneOf is store.CanonicalLaneOrder membership by wire value. It
// is the store's own fixed set rather than a UI copy of it, so a lane the
// store adds is accepted here without a second edit.
func canonicalLaneOf(raw string) (store.Lane, bool) {
	for _, lane := range store.CanonicalLaneOrder {
		if string(lane) == raw {
			return lane, true
		}
	}
	return "", false
}

// resolveProductTaskScope turns a parsed scope into a store scope whose
// container belongs to productID.
//
// The membership check runs here, against the product's own delivery
// listing, rather than being left to the store -- so a container id from
// another product is a named result the handler can render, never a query
// whose answer would be some other product's rows. The store's own
// ErrMilestoneOutsideProduct guard stays: this layer is the UI's first
// line, not a replacement for the read's.
//
// A single-container mode with no id picks the product's HIGHEST-position
// container of that kind, which is what FR 7191dba1 means by "the first
// milestone in the Milestones table order (highest position)".
//
// That is the listing's LAST milestone, not its first:
// slice.ListProductDelivery returns position-ASCENDING, while the spec's
// default, the store's own product-wide read (ORDER BY m.position DESC)
// and the board's swimlane order (FR cf000440) all take the highest
// position first. So the listing is walked from the end, and a milepebble
// comes from the highest-position milestone that has one. The product-wide
// mode names no container and reads no listing at all.
//
// In milepebble mode the resolver settles two containers, not one. The
// URL's own container is the milepebble the read is scoped to; the
// PARENT milestone is settled alongside it, because the mode's second
// select offers that milestone's milepebbles and the milepebble id alone
// cannot say which ones. The URL may name the parent explicitly (that is
// what changing the milestone select submits); otherwise it is derived
// from the milepebble itself, and a milepebble named without a parent
// therefore still gets a marked, populated milestone select.
func (app *App) resolveProductTaskScope(ctx context.Context, productID uuid.UUID, parsed productTaskScope) (resolvedProductTaskScope, productTaskScopeProblem) {
	out := resolvedProductTaskScope{
		Parsed: parsed,
		Store:  store.ProductTaskScope{Kind: parsed.Kind},
	}
	requires := store.ProductTaskScope{Kind: parsed.Kind}
	if !requires.RequiresContainer() {
		return out, productTaskScopeOK
	}

	listing, err := app.spec.Delivery(ctx, productID, nil)
	if err != nil {
		logger.Error("product task scope: delivery listing read failed",
			"product", productID.String(), "error", err)
		return out, productTaskScopeUnreadable
	}
	out.Milestones = taskContainersOf(listing)

	if parsed.Kind == store.ProductTaskScopeMilepebble {
		return resolveMilepebbleScope(out, listing, parsed)
	}

	id := parsed.ContainerID
	if id == uuid.Nil {
		id = firstContainerOfKind(listing, parsed.Kind)
		if id == uuid.Nil {
			return out, productTaskScopeNoContainers
		}
	}

	container, found := resolveTaskContainer(listing, id)
	if !found || container.Kind != string(parsed.Kind) {
		// An id this product owns but under the other kind is as wrong as
		// one it does not own at all: the mode names what it wants, and
		// answering with the other kind's tasks would show rows the URL
		// did not ask for.
		return out, productTaskScopeNotFound
	}
	out.Store.ContainerID = id
	out.Container = container
	out.Milestone = container
	return out, productTaskScopeOK
}

// resolveMilepebbleScope settles a milepebble scope's two containers: the
// milepebble the read is scoped to, and the parent milestone whose
// milepebbles the control offers.
//
// The parent is resolved first and independently, because it answers a
// different question from the container: the URL may name a milestone
// whose milepebbles are on offer without naming one of them yet (that is
// exactly what choosing a milestone in the control submits), and in that
// case the first milepebble of that milestone is selected -- the same
// no-id rule the milestone mode applies, one level down.
//
// A named parent is membership-checked like any other id, and so is the
// named milepebble -- an id the product does not own, or one that is not a
// milepebble, is still a not-found.
//
// A milepebble this product DOES own but that hangs under a different
// milestone than the named parent is not a refusal: it is the pair the
// control's own milestone change necessarily submits. The form is a plain
// GET over two selects, and a select cannot be emptied by choosing
// something else in it, so changing the Milestone select sends the PREVIOUS
// milepebble alongside the new milestone every time. Refusing that pair
// breaks the one interaction the `milestone` parameter exists to enable --
// and over htmx, which does not swap on a 4xx, it breaks silently, leaving
// the operator clicking a control that appears to do nothing. So the named
// milestone wins: it is the more specific statement of what the operator is
// looking at, and the no-id rule this mode already applies settles the
// milepebble one level down.
func resolveMilepebbleScope(out resolvedProductTaskScope, listing slice.DeliveryListing, parsed productTaskScope) (resolvedProductTaskScope, productTaskScopeProblem) {
	parent, problem := resolveMilepebbleParent(listing, parsed.MilestoneID)
	if problem != productTaskScopeOK {
		return out, problem
	}
	switch {
	case parent.ID != uuid.Nil:
		// The URL named the milestone whose milepebbles are on offer.
	case parsed.ContainerID != uuid.Nil:
		// It named only a milepebble, so its own parent is the offer.
		parent, problem = milestoneOfMilepebble(listing, parsed.ContainerID)
		if problem != productTaskScopeOK {
			return out, problem
		}
	default:
		// Neither: the no-id default, the highest-position milestone that
		// has a milepebble at all.
		parentID := firstContainerOfKind(listing, store.ProductTaskScopeMilepebble)
		if parentID != uuid.Nil {
			parent, problem = milestoneOfMilepebble(listing, parentID)
			if problem != productTaskScopeOK {
				return out, problem
			}
			break
		}
		// Nothing anywhere is cut. The product may still have milestones,
		// and then the highest-position one is what this mode should mark
		// and offer from -- the container step below reports the emptiness.
		parentID = firstContainerOfKind(listing, store.ProductTaskScopeMilestone)
		if parentID == uuid.Nil {
			// The product genuinely has nothing to scope to.
			return out, productTaskScopeNoContainers
		}
		parent, problem = resolveMilepebbleParent(listing, parentID)
		if problem != productTaskScopeOK {
			return out, problem
		}
	}
	out.Milestone = parent

	// The container is settled in two steps. The named one is checked
	// against the product's own listing first, so an id the product does
	// not own is a not-found no matter what the parent says -- and only
	// then is the pair's disagreement considered.
	if parsed.ContainerID != uuid.Nil {
		named, found := resolveTaskContainer(listing, parsed.ContainerID)
		if !found || named.Kind != string(store.MilestoneKindMilepebble) {
			return out, productTaskScopeNotFound
		}
		if isMilepebbleOf(named.ID, parent) {
			out.Store.ContainerID = named.ID
			out.Container = named
			return out, productTaskScopeOK
		}
		// The pair disagrees -- this product's own milepebble, but not the
		// named milestone's. Fall through to the named milestone's first.
	}

	if len(parent.Milepebbles) == 0 {
		// A named milestone of a real product that simply has nothing cut
		// under it. Distinct from the product having no milestones at all:
		// here the operator has named a real milestone, and the milestone
		// select -- which lets them pick a cut one -- has to stay.
		return out, productTaskScopeNoMilepebbles
	}
	// The named milestone's own first milepebble, which is the only thing
	// this mode can sensibly show before one is chosen -- and which the
	// listing that built the parent vouches for.
	chosen := parent.Milepebbles[0]
	out.Store.ContainerID = chosen.ID
	out.Container = taskContainer{
		ID:     chosen.ID,
		Name:   chosen.Name,
		Kind:   string(store.MilestoneKindMilepebble),
		Status: chosen.Status,
	}
	return out, productTaskScopeOK
}

// resolveMilepebbleParent is the milestone a milepebble scope's options
// come from. A named id is membership-checked against the product's own
// milestones; uuid.Nil means the URL named none, which the caller answers
// from the listing. A non-nil problem is a not-found: an id this product
// does not own, or one that is a milepebble rather than a milestone.
func resolveMilepebbleParent(listing slice.DeliveryListing, id uuid.UUID) (taskContainer, productTaskScopeProblem) {
	if id == uuid.Nil {
		return taskContainer{}, productTaskScopeOK
	}
	for _, m := range listing.Milestones {
		if m.ID != id {
			continue
		}
		c := taskContainer{ID: m.ID, Name: m.Name, Kind: string(store.MilestoneKindMilestone), Status: m.Status}
		for _, mp := range m.Milepebbles {
			c.Milepebbles = append(c.Milepebbles, taskContainerChild{ID: mp.ID, Name: mp.Name, Status: mp.Status})
		}
		return c, productTaskScopeOK
	}
	return taskContainer{}, productTaskScopeNotFound
}

// milestoneOfMilepebble is the milestone a milepebble belongs to.
func milestoneOfMilepebble(listing slice.DeliveryListing, id uuid.UUID) (taskContainer, productTaskScopeProblem) {
	for _, m := range listing.Milestones {
		for _, mp := range m.Milepebbles {
			if mp.ID == id {
				return resolveMilepebbleParent(listing, m.ID)
			}
		}
	}
	return taskContainer{}, productTaskScopeNotFound
}

// isMilepebbleOf reports whether a milepebble is one of parent's own
// children.
func isMilepebbleOf(id uuid.UUID, parent taskContainer) bool {
	for _, mp := range parent.Milepebbles {
		if mp.ID == id {
			return true
		}
	}
	return false
}

// taskContainersOf is the listing's milestones as containers, highest
// position first -- the order the Milestones table and the scope control's
// own milestone select read in, so the default a no-id mode resolves to is
// the first option the operator is offered. That is the reverse of the
// listing's own position-ASCENDING order.
//
// Each carries its own status and its children, so the control can mark a
// container the all-incomplete scope excludes without a second read.
func taskContainersOf(listing slice.DeliveryListing) []taskContainer {
	out := make([]taskContainer, 0, len(listing.Milestones))
	for i := len(listing.Milestones) - 1; i >= 0; i-- {
		m := listing.Milestones[i]
		c := taskContainer{ID: m.ID, Name: m.Name, Kind: string(store.MilestoneKindMilestone), Status: m.Status}
		for _, mp := range m.Milepebbles {
			c.Milepebbles = append(c.Milepebbles, taskContainerChild{ID: mp.ID, Name: mp.Name, Status: mp.Status})
		}
		out = append(out, c)
	}
	return out
}

// firstContainerOfKind is the container a single-container mode with no id
// falls back to: the highest-position one of that kind (see
// resolveProductTaskScope's own note on why that is the listing's last
// entry). uuid.Nil means the product has none of that kind, which the
// caller reports as its own empty result rather than as a not-found.
func firstContainerOfKind(listing slice.DeliveryListing, kind store.ProductTaskScopeKind) uuid.UUID {
	for i := len(listing.Milestones) - 1; i >= 0; i-- {
		m := listing.Milestones[i]
		if kind == store.ProductTaskScopeMilestone {
			return m.ID
		}
		for _, mp := range m.Milepebbles {
			return mp.ID
		}
	}
	return uuid.Nil
}

// readProductTasks turns a resolved scope into one page of rows and the
// total behind them.
//
// Both come from the same store.ListProductTasksParams value, passed to the
// list and the count unchanged. That is the whole point: the count is the
// store's own answer for the same filters, so "Showing X of Y tasks" can
// never disagree with the rows above it -- two separately-built parameter
// sets are exactly how that happens.
func (app *App) readProductTasks(ctx context.Context, productID uuid.UUID, scope resolvedProductTaskScope) (productTaskPage, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		logger.Warn("product task read: could not resolve scope", "error", err)
		return productTaskPage{}, fmt.Errorf("resolve scope: %w", err)
	}

	params := store.ListProductTasksParams{
		ScopeID:   scopeID,
		ProductID: productID,
		Scope:     scope.Store,
		Lane:      scope.Parsed.Lane,
		OnlyStuck: scope.Parsed.OnlyStuck,
		Page: store.PageParams{
			PageSize:          scope.Parsed.PageSize,
			ContinuationToken: scope.Parsed.PageToken,
		},
	}

	page, err := app.tasks.ListProductTasks(ctx, params)
	if err != nil {
		return productTaskPage{}, fmt.Errorf("list product tasks: %w", err)
	}
	total, err := app.tasks.CountProductTasks(ctx, params)
	if err != nil {
		return productTaskPage{}, fmt.Errorf("count product tasks: %w", err)
	}
	return productTaskPage{
		Rows:      page.Items,
		Total:     total,
		NextToken: page.NextToken,
		PageToken: scope.Parsed.PageToken,
	}, nil
}
