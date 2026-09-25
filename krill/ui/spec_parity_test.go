package main

// Wire-driven field parity between each spec view and the MCP tool it
// mirrors. The acceptance for this lane is "matches
// get_product_slice/list_personas/list_non_goals" -- so these tests do not
// re-state the expected strings; they reflect over the *actual* MCP wire
// types (slice.Document's entities, tools.PersonaSummary,
// tools.NonGoalSummary) and, for every field the view is supposed to carry,
// assert that field's own value is present in the rendered HTML. A field
// added to, removed from, or renamed on a wire type -- or dropped by a
// template -- fails here without the test being edited.
//
// Two layers:
//
//   - TestSpecViewsRenderEveryCarriedWireField: the field-by-field presence
//     check, scoped to the fields each view claims to carry.
//   - TestSpecWireFieldClassesAreComplete: a reflection guard that fails if a
//     wire struct exposes a leaf field the view has not explicitly classified
//     as carried, grouped, or structural, forcing a decision on every new
//     field the wire grows.
//
// The render under test is the pure builder + template -- the same
// capabilityPageOf/decisionsPageOf/personasPageOf/nonGoalsPageOf the HTTP
// handlers call -- so the whole HTML the browser receives is exercised
// without a database. The builders take the wire/store structs directly,
// which is exactly what the MCP tools marshal, so the two are proven to
// agree field-for-field on the same source rows.

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// wireFieldClass is how one leaf field of an MCP wire struct relates to the
// rendered view.
type wireFieldClass int

const (
	// wireCarried: the field's value must appear verbatim in the rendered
	// HTML for that entity.
	wireCarried wireFieldClass = iota
	// wireGrouped: the field selects which section the entity renders under
	// (the non-goal kind -> permanent/deferred bucket) rather than appearing
	// as a literal token; the test asserts the grouping, not the raw string.
	wireGrouped
	// wireStructural: an internal identifier the view does not surface --
	// scope/revision/parent ids and ordering positions are machine plumbing,
	// not operator-facing data. Listed explicitly so adding one to a wire
	// type is a deliberate, reviewed decision.
	wireStructural
)

// wireClasses maps a wire struct's reflect.Type to the classification of each
// of its leaf fields (JSON tag name -> class). TestSpecWireFieldClassesAreComplete
// fails if a wire type exposes a leaf field absent from this table, so the
// moment slice/tools grows a field, someone must decide whether the view
// carries, groups, or structurally ignores it.
var wireClasses = map[string]map[string]wireFieldClass{
	// get_product_slice's decision entity (FR 6aa70e3a).
	"DecisionEntity": {
		"id":             wireCarried, // EntityRef.ID
		"revision_id":    wireStructural,
		"feature_set_id": wireStructural,
		"name":           wireCarried,
		"body":           wireCarried, // full, untruncated
		"position":       wireStructural,
		"display_number": wireCarried, // LBn
	},
	// get_product_slice's feature-set entity (FR 638a7e5f).
	"FeatureSetEntity": {
		"id":          wireCarried,
		"revision_id": wireStructural,
		"product_id":  wireStructural,
		"name":        wireCarried,
		"description": wireCarried,
		"position":    wireStructural,
	},
	// get_product_slice's feature entity (FR 638a7e5f) -- Cn is the
	// DisplayNumber a reader cites.
	"FeatureEntity": {
		"id":             wireCarried,
		"revision_id":    wireStructural,
		"feature_set_id": wireStructural,
		"name":           wireCarried,
		"description":    wireCarried,
		"position":       wireStructural,
		"display_number": wireCarried,
	},
	// get_product_slice's requirement entity (FR 638a7e5f).
	"RequirementEntity": {
		"id":          wireCarried,
		"revision_id": wireStructural,
		"feature_id":  wireStructural,
		"kind":        wireCarried, // FR / NFR
		"name":        wireCarried,
		"body":        wireCarried,
		"position":    wireStructural,
	},
	// list_personas' wire (FR b4c1c77f).
	"PersonaSummary": {
		"id":          wireCarried,
		"name":        wireCarried,
		"description": wireCarried,
	},
	// list_non_goals' wire (FR b4c1c77f). kind is grouped into the
	// permanent/deferred sections rather than shown as a raw token.
	"NonGoalSummary": {
		"id":   wireCarried,
		"kind": wireGrouped,
		"name": wireCarried,
		"body": wireCarried,
	},
}

// jsonName is a struct field's JSON tag name (or lowercased Go name when
// untagged), which is how the MCP wire names the field on the wire.
func jsonName(f reflect.StructField) string {
	tag := strings.Split(f.Tag.Get("json"), ",")[0]
	if tag == "" {
		return strings.ToLower(f.Name)
	}
	return tag
}

// wireLeaves flattens a wire struct into its leaf fields by JSON name ->
// value, recursing through embedded structs (EntityRef). Pointer-to-string
// optional fields resolve to their pointee (a nil optional carries no
// operator-facing text and is skipped); uuid.UUID values resolve via their
// String form.
func wireLeaves(t *testing.T, v any) map[string]string {
	t.Helper()
	out := map[string]string{}
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		tv := v
		if tv.Kind() == reflect.Ptr {
			if tv.IsNil() {
				return
			}
			tv = tv.Elem()
		}
		if tv.Kind() != reflect.Struct {
			return
		}
		rt := tv.Type()
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			fv := tv.Field(i)
			if !fv.CanInterface() {
				continue
			}
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				walk(fv) // embedded EntityRef: its fields are top-level
				continue
			}
			out[jsonName(f)] = wireScalarString(t, fv)
		}
	}
	walk(reflect.ValueOf(v))
	return out
}

// wireScalarString renders a leaf value as the string the template must show.
// uuid.UUID (an array) and named string types go through fmt.Stringer;
// numbers via strconv; optional *string via its pointee.
func wireScalarString(t *testing.T, v reflect.Value) string {
	t.Helper()
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return ""
		}
		return wireScalarString(t, v.Elem())
	}
	if v.CanInterface() {
		if s, ok := v.Interface().(fmt.Stringer); ok {
			return s.String()
		}
	}
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	}
	return fmt.Sprint(v.Interface())
}

// classOf returns the classification of field name on the wire type named
// typeName, and whether it was classified at all.
func classOf(typeName, field string) (wireFieldClass, bool) {
	byField, ok := wireClasses[typeName]
	if !ok {
		return 0, false
	}
	c, ok := byField[field]
	return c, ok
}

// requireCarried asserts that for every field on the wire struct typeName
// classified wireCarried and holding a non-empty value, that exact value
// appears in html. Field-scoped so a dropped field is named precisely.
func requireCarried(t *testing.T, typeName string, entity any, html string) {
	t.Helper()
	for field, value := range wireLeaves(t, entity) {
		class, ok := classOf(typeName, field)
		if !ok || class != wireCarried {
			continue
		}
		if value == "" {
			continue // nil optional text: nothing to render
		}
		if !strings.Contains(html, value) {
			t.Errorf("%s: carried wire field %q with value %q is not rendered", typeName, field, value)
		}
	}
}

// TestSpecWireFieldClassesAreComplete is the drift guard: every leaf field
// each wire struct exposes must be classified in wireClasses. If a wire type
// gains a field and nobody decides how the view treats it, this fails --
// keeping the carried/structural lists honest as the wire evolves.
func TestSpecWireFieldClassesAreComplete(t *testing.T) {
	wires := map[string]any{
		"FeatureSetEntity":  slice.FeatureSetEntity{},
		"FeatureEntity":     slice.FeatureEntity{},
		"RequirementEntity": slice.RequirementEntity{},
		"DecisionEntity":    slice.DecisionEntity{},
		"PersonaSummary":    tools.PersonaSummary{},
		"NonGoalSummary":    tools.NonGoalSummary{},
	}

	for typeName, zero := range wires {
		byField, ok := wireClasses[typeName]
		if !ok {
			t.Errorf("wire type %s has no field classification table", typeName)
			continue
		}
		for field := range wireLeaves(t, zero) {
			if _, ok := byField[field]; !ok {
				t.Errorf("wire type %s exposes field %q with no classification; decide carried/grouped/structural", typeName, field)
			}
		}
		for field := range byField {
			if _, ok := wireLeaves(t, zero)[field]; !ok {
				t.Errorf("wireClasses lists field %q for %s, but the type has no such field", field, typeName)
			}
		}
	}
}

// uniqueBody is a long body string whose tail is a marker, so a view that
// renders only a truncated prefix fails presence-of-full-body assertions.
func uniqueBody(prefix string) *string {
	s := prefix + " " + strings.Repeat("filler sentence. ", 20) + "END-MARKER-" + prefix
	return &s
}

// TestCapabilityMapCarriesEveryWireField (FR 638a7e5f): a product's
// capability map renders every field get_product_slice returns for its
// feature sets, features, and requirements -- id, name, description, the
// feature's Cn (DisplayNumber), the requirement's FR/NFR kind, and full
// bodies -- driven from the slice wire structs the tool marshals unchanged.
func TestCapabilityMapCarriesEveryWireField(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	fsID := mustID(t, "22222222-2222-2222-2222-222222222222")
	featID := mustID(t, "33333333-3333-3333-3333-333333333333")
	frID := mustID(t, "44444444-4444-4444-4444-444444444444")
	nfrID := mustID(t, "55555555-5555-5555-5555-555555555555")

	// The wire: the exact entities get_product_slice returns (FR8 populates
	// all six, FR/NFR discriminated by Kind).
	fs := slice.FeatureSetEntity{
		EntityRef:   slice.EntityRef{ID: fsID},
		ProductID:   productID,
		Name:        "Spec axis",
		Description: ptr("feature sets about the spec"),
	}
	feat := slice.FeatureEntity{
		EntityRef:     slice.EntityRef{ID: featID},
		FeatureSetID:  fsID,
		Name:          "Scoped slice",
		Description:   ptr("one query, four granularities"),
		DisplayNumber: 7,
	}
	fr := slice.RequirementEntity{
		EntityRef: slice.EntityRef{ID: frID},
		FeatureID: featID,
		Kind:      "FR",
		Name:      "product granularity",
		Body:      uniqueBody("fr"),
	}
	nfr := slice.RequirementEntity{
		EntityRef: slice.EntityRef{ID: nfrID},
		FeatureID: featID,
		Kind:      "NFR",
		Name:      "stays cheap",
		Body:      uniqueBody("nfr"),
	}
	doc := slice.Document{
		Product:     &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID}, Name: "krill", Vision: "the substrate"},
		FeatureSets: []slice.FeatureSetEntity{fs},
		Features:    []slice.FeatureEntity{feat},
		Requirements: []slice.RequirementEntity{
			fr, nfr,
		},
	}

	html := string(renderPage(capabilityTemplate, capabilityPageOf(doc, productID)))

	requireCarried(t, "FeatureSetEntity", fs, html)
	requireCarried(t, "FeatureEntity", feat, html)
	requireCarried(t, "RequirementEntity", fr, html)
	requireCarried(t, "RequirementEntity", nfr, html)

	// The product header the map is browsed under.
	for _, want := range []string{"krill", "the substrate"} {
		if !strings.Contains(html, want) {
			t.Errorf("capability map product header missing %q", want)
		}
	}
}

// TestDecisionsCarryEveryWireField (FR 6aa70e3a): the decisions list renders
// every field DecisionEntity exposes -- id, name, the LBn DisplayNumber, and
// the FULL untruncated body -- driven from the wire struct.
func TestDecisionsCarryEveryWireField(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	dID := mustID(t, "66666666-6666-6666-6666-666666666666")
	body := uniqueBody("lb1")

	d := slice.DecisionEntity{
		EntityRef:     slice.EntityRef{ID: dID},
		FeatureSetID:  mustID(t, "22222222-2222-2222-2222-222222222222"),
		Name:          "One document type",
		Body:          body,
		DisplayNumber: 1,
	}
	doc := slice.Document{
		Product:   &slice.ProductEntity{Name: "krill"},
		Decisions: []slice.DecisionEntity{d},
	}

	html := string(renderPage(decisionsTemplate, decisionsPageOf(doc, productID)))
	requireCarried(t, "DecisionEntity", d, html)

	// Full body, not a truncated prefix: the whole uniqueBody, tail included.
	if !strings.Contains(html, *body) {
		t.Errorf("decisions page did not render the full decision body (tail marker missing)")
	}
	// The LBn citation prefix.
	if !strings.Contains(html, "LB1") {
		t.Errorf("decisions page missing LB1 DisplayNumber citation")
	}
}

// TestPersonasCarryEveryWireField (FR b4c1c77f): each persona renders every
// field list_personas returns -- id, name, description -- driven from the
// store.Persona the tool marshals into tools.PersonaSummary.
func TestPersonasCarryEveryWireField(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	pID := mustID(t, "77777777-7777-7777-7777-777777777777")
	product := store.Product{ID: productID, Name: "krill", Vision: "the substrate"}
	persona := store.Persona{ID: pID, Name: "Operator", Description: ptr("runs the deployment")}

	// The wire list_personas would return for this row.
	wire := tools.PersonaSummary{ID: persona.ID.String(), Name: persona.Name, Description: persona.Description}

	html := string(renderPage(personasTemplate, personasPageOf(product, []store.Persona{persona}, productID)))
	requireCarried(t, "PersonaSummary", wire, html)
}

// TestNonGoalsCarryEveryWireField (FR b4c1c77f): each non-goal renders every
// field list_non_goals returns -- id, name, body -- and the page clearly
// distinguishes the permanent kind from the deferred kind. Driven from the
// store.NonGoal rows the tool marshals into tools.NonGoalSummary.
func TestNonGoalsCarryEveryWireField(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	permID := mustID(t, "88888888-8888-8888-8888-888888888888")
	defID := mustID(t, "99999999-9999-9999-9999-999999999999")
	product := store.Product{ID: productID, Name: "krill"}
	perm := store.NonGoal{ID: permID, Kind: store.NonGoalKindPermanent, Name: "No web editing", Body: uniqueBody("perm")}
	def := store.NonGoal{ID: defID, Kind: store.NonGoalKindDeferred, Name: "History in the UI", Body: uniqueBody("def")}

	permWire := tools.NonGoalSummary{ID: perm.ID.String(), Kind: string(perm.Kind), Name: perm.Name, Body: perm.Body}
	defWire := tools.NonGoalSummary{ID: def.ID.String(), Kind: string(def.Kind), Name: def.Name, Body: def.Body}

	html := string(renderPage(nonGoalsTemplate, nonGoalsPageOf(product, []store.NonGoal{perm, def}, productID)))
	requireCarried(t, "NonGoalSummary", permWire, html)
	requireCarried(t, "NonGoalSummary", defWire, html)

	// The kind is surfaced as two distinct sections (wireGrouped): permanent
	// first, deferred second, each holding only its own kind.
	if !strings.Contains(html, "Permanent non-goals") || !strings.Contains(html, "Deferred, not foreclosed") {
		t.Errorf("non-goals page does not distinguish the permanent and deferred kinds")
	}
	permAt := strings.Index(html, "No web editing")
	defAt := strings.Index(html, "History in the UI")
	permHeading := strings.Index(html, "Permanent non-goals")
	defHeading := strings.Index(html, "Deferred, not foreclosed")
	if permAt < permHeading || (defHeading >= 0 && defAt < defHeading) {
		t.Errorf("non-goals rendered under the wrong kind section (perm@%d/%d def@%d/%d)",
			permAt, permHeading, defAt, defHeading)
	}
}

// TestCarriedWireFieldPresenceIsNonVacuous guards the parity tests themselves:
// it drops a field's value from a copy of the wire and confirms the same
// requireCarried check the real tests use flags it. Without this, a bug that
// made every wire field render as empty (or made requireCarried a no-op)
// would silently pass the real tests.
func TestCarriedWireFieldPresenceIsNonVacuous(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	fsID := mustID(t, "22222222-2222-2222-2222-222222222222")
	featID := uuid.New()
	frID := uuid.New()

	fs := slice.FeatureSetEntity{EntityRef: slice.EntityRef{ID: fsID}, Name: "Spec axis", Description: ptr("about the spec")}
	feat := slice.FeatureEntity{EntityRef: slice.EntityRef{ID: featID}, FeatureSetID: fsID, Name: "Scoped slice", Description: ptr("one query"), DisplayNumber: 7}
	fr := slice.RequirementEntity{EntityRef: slice.EntityRef{ID: frID}, FeatureID: featID, Kind: "FR", Name: "granularity", Body: ptr("returns every child")}
	doc := slice.Document{Product: &slice.ProductEntity{Name: "krill"}, FeatureSets: []slice.FeatureSetEntity{fs}, Features: []slice.FeatureEntity{feat}, Requirements: []slice.RequirementEntity{fr}}

	html := string(renderPage(capabilityTemplate, capabilityPageOf(doc, productID)))

	// Rename a field's value in the wire but keep the rendered HTML from the
	// original: requireCarried must now report the mismatch. We assert this
	// by running requireCarried against a fresh sub-test whose entity value
	// differs from what was rendered.
	renamed := fs
	renamed.Name = "a name the page does not contain"
	fake := &testing.T{}
	requireCarried(fake, "FeatureSetEntity", renamed, html)
	if !fake.Failed() {
		t.Errorf("requireCarried did not flag a carried field whose value is absent from the render")
	}
}
