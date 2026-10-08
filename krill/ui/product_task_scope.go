// The product-wide task read layer shared by the Tasks table and the Board:
// scope parsing, container membership resolution, and the paged read.
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

// Query parameters, named as krill api's GET /products/{id}/tasks names them,
// so a copied link is the same request the api answers.
const (
	productTaskScopeParam     = pages.ProductTaskScopeQueryParam
	productTaskContainerParam = pages.ProductTaskContainerQueryParam
	productTaskLaneParam      = pages.ProductTaskLaneQueryParam
	productTaskOnlyStuckParam = pages.ProductTaskOnlyStuckQueryParam
	productTaskPageSizeParam  = "page_size"
	productTaskPageTokenParam = "page_token"

	// productTaskMilestoneParam names the parent milestone whose milepebbles the
	// select offers. UI-only: it never reaches the store.
	productTaskMilestoneParam = pages.ProductTaskMilestoneQueryParam
)

// productTaskScopeQuery is the scope query naming one container, with the
// mode taken from the container's own kind so milepebbles scope correctly.
func productTaskScopeQuery(c taskContainer) url.Values {
	mode := store.ProductTaskScopeMilestone
	if c.Kind == string(store.MilestoneKindMilepebble) {
		mode = store.ProductTaskScopeMilepebble
	}
	return url.Values{
		productTaskScopeParam:     []string{string(mode)},
		productTaskContainerParam: []string{c.ID.String()},
	}
}

// productTaskContainerHref is the Tasks or Board URL scoped to one container.
func productTaskContainerHref(pid uuid.UUID, suffix string, c taskContainer) string {
	return productHref(pid, suffix) + "?" + productTaskScopeQuery(c).Encode()
}

// productTaskScope is the scope control's state, parsed from the query only.
// Kind uses the store's scope kinds; ContainerID is resolved against the product
// before any read.
type productTaskScope struct {
	// Kind is always filled by parseProductTaskScope.
	Kind store.ProductTaskScopeKind

	// ContainerID is uuid.Nil when none was chosen; the resolver then picks one.
	ContainerID uuid.UUID

	// MilestoneID is the milepebble select's parent milestone; milepebble mode only,
	// never sent to the store. uuid.Nil derives it from the selected milepebble.
	MilestoneID uuid.UUID

	// Lane is the optional lane filter. nil is every lane, never "no lanes".
	Lane *store.Lane

	OnlyStuck bool

	// PageSize and PageToken are the raw paging pair; the store clamps PageSize
	// so this layer and the api agree.
	PageSize  int
	PageToken string
}

// productTaskScopeProblem is a scope's named failure, each mapping to a
// distinct answer: 400, 404, an empty result, or a read failure.
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
	// productTaskScopeNoMilepebbles is milepebble mode on a product whose milestones
	// are all uncut; the milestone select stays so a cut one can be picked.
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

// resolvedProductTaskScope is a parsed scope whose container is verified
// against the product's delivery listing.
type resolvedProductTaskScope struct {
	// Parsed is the scope as the URL spelled it, for the controls to read back.
	Parsed productTaskScope

	// Store is the scope to read with; its ContainerID differs from Parsed's only
	// when the resolver picked one for an id-less mode.
	Store store.ProductTaskScope

	// Container is the resolved container; zero in the product-wide scope.
	Container taskContainer

	// Milestone is the milestone select's selection: the container itself in
	// milestone mode, the milepebble's parent in milepebble mode.
	Milestone taskContainer

	// Milestones are the product's milestones for the control's options; empty
	// in the product-wide scope.
	Milestones []taskContainer
}

// productTaskPage is one page of rows plus the total, read for the same
// filters rather than derived from the rows.
type productTaskPage struct {
	Rows      []store.ProductTaskRow
	Total     int
	NextToken string

	// PageToken is the token that produced this page; empty on the first page.
	PageToken string
}

// parseProductTaskScope reads the scope off the query. Absent scope is
// ProductTaskScopeIncomplete (as in the api); any malformed value is rejected
// as productTaskScopeInvalid before any read.
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
		// An empty id means none chosen yet; a non-UUID is a mistyped id and refused.
		if id := strings.TrimSpace(q.Get(productTaskContainerParam)); id != "" {
			parsed, err := uuid.Parse(id)
			if err != nil {
				return productTaskScope{}, productTaskScopeInvalid
			}
			scope.ContainerID = parsed
		}
		// The parent milestone is parsed and refused the same way; only milepebble
		// mode reads it.
		if raw := strings.TrimSpace(q.Get(productTaskMilestoneParam)); raw != "" {
			parsed, err := uuid.Parse(raw)
			if err != nil {
				return productTaskScope{}, productTaskScopeInvalid
			}
			scope.MilestoneID = parsed
		}
	}
	// A container id under the product-wide scope is ignored, matching the store,
	// api and MCP; refusing it would break switching back from a scoped link.

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

// canonicalLaneOf matches against store.CanonicalLaneOrder, so a new store
// lane is accepted without a UI edit.
func canonicalLaneOf(raw string) (store.Lane, bool) {
	for _, lane := range store.CanonicalLaneOrder {
		if string(lane) == raw {
			return lane, true
		}
	}
	return "", false
}

// resolveProductTaskScope checks the container belongs to productID, so a foreign
// id is a named not-found rather than another product's rows. An id-less mode
// picks the highest-position container: the listing's last, as it is ascending.
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
		// An owned id of the other kind is also not-found; the mode names what it wants.
		return out, productTaskScopeNotFound
	}
	out.Store.ContainerID = id
	out.Container = container
	out.Milestone = container
	return out, productTaskScopeOK
}

// resolveMilepebbleScope settles the parent milestone and the milepebble. A
// milepebble under a different milestone than the named parent is not refused:
// changing the milestone select always resubmits the old milepebble, so it wins.
func resolveMilepebbleScope(out resolvedProductTaskScope, listing slice.DeliveryListing, parsed productTaskScope) (resolvedProductTaskScope, productTaskScopeProblem) {
	parent, problem := resolveMilepebbleParent(listing, parsed.MilestoneID)
	if problem != productTaskScopeOK {
		return out, problem
	}
	containerID := parsed.ContainerID
	if containerID != uuid.Nil {
		// Switching from Milestone mode submits the milestone as the container; treat
		// it as the parent and offer its milepebbles.
		if asMilestone, p := resolveMilepebbleParent(listing, containerID); p == productTaskScopeOK {
			if parent.ID == uuid.Nil {
				parent = asMilestone
			}
			containerID = uuid.Nil
		}
	}
	switch {
	case parent.ID != uuid.Nil:
		// The URL named the milestone whose milepebbles are on offer.
	case containerID != uuid.Nil:
		// It named only a milepebble, so its own parent is the offer.
		parent, problem = milestoneOfMilepebble(listing, containerID)
		if problem != productTaskScopeOK {
			return out, problem
		}
	default:
		// Neither: the highest-position milestone that has a milepebble.
		parentID := firstContainerOfKind(listing, store.ProductTaskScopeMilepebble)
		if parentID != uuid.Nil {
			parent, problem = milestoneOfMilepebble(listing, parentID)
			if problem != productTaskScopeOK {
				return out, problem
			}
			break
		}
		// Nothing is cut; mark the highest-position milestone and let the container
		// step report the emptiness.
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

	// Check the named milepebble against the listing first, so a foreign id is
	// not-found regardless of the parent.
	if containerID != uuid.Nil {
		named, found := resolveTaskContainer(listing, containerID)
		if !found || named.Kind != string(store.MilestoneKindMilepebble) {
			return out, productTaskScopeNotFound
		}
		if isMilepebbleOf(named.ID, parent) {
			out.Store.ContainerID = named.ID
			out.Container = named
			return out, productTaskScopeOK
		}
		// Owned milepebble, but not the named milestone's: use the milestone's first.
	}

	if len(parent.Milepebbles) == 0 {
		// The named milestone has nothing cut; distinct from no milestones at all.
		return out, productTaskScopeNoMilepebbles
	}
	// Before one is chosen, show the named milestone's first milepebble.
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

// resolveMilepebbleParent membership-checks a named milestone id; uuid.Nil
// means none was named. A problem is a not-found (foreign id or a milepebble).
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

// isMilepebbleOf reports whether id is one of parent's milepebbles.
func isMilepebbleOf(id uuid.UUID, parent taskContainer) bool {
	for _, mp := range parent.Milepebbles {
		if mp.ID == id {
			return true
		}
	}
	return false
}

// taskContainersOf lists milestones highest position first (the reverse of
// the listing), with status and children for the scope control.
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

// firstContainerOfKind is the highest-position container of a kind (the
// listing's last). uuid.Nil means the product has none of that kind.
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

// readProductTasks reads one page and its total from the same params value,
// so "Showing X of Y" can never disagree with the rows.
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
