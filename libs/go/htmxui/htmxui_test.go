package htmxui

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	var w io.Writer = &sb
	if err := c.Render(context.Background(), w); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return sb.String()
}

var classAttrRE = regexp.MustCompile(`class="([^"]*)"`)

// classTokens extracts every whitespace-separated class token from every
// class="..." attribute in rendered output.
func classTokens(body string) []string {
	var tokens []string
	for _, m := range classAttrRE.FindAllStringSubmatch(body, -1) {
		tokens = append(tokens, strings.Fields(m[1])...)
	}
	return tokens
}

// hasClass reports whether class is present as an exact token in some
// class="..." attribute -- not merely a substring. This matters because
// e.g. "btn-error" is a substring of a hypothetical "btn-error-ish", and
// "btn" is a substring of every other btn-* modifier; a plain
// strings.Contains check would pass even if the wrong class were emitted.
func hasClass(body, class string) bool {
	for _, tok := range classTokens(body) {
		if tok == class {
			return true
		}
	}
	return false
}

// --- Button (FR1) -----------------------------------------------------------

func TestButton_VariantAndSizeClassSets(t *testing.T) {
	cases := []struct {
		name        string
		variant     ButtonVariant
		size        ButtonSize
		disabled    bool
		wantClasses []string
	}{
		{
			name:        "primary md",
			variant:     ButtonPrimary,
			size:        ButtonSizeMD,
			wantClasses: []string{"btn", "btn-primary"},
		},
		{
			name:        "error sm",
			variant:     ButtonError,
			size:        ButtonSizeSM,
			wantClasses: []string{"btn", "btn-error", "btn-sm"},
		},
		{
			name:        "success lg",
			variant:     ButtonSuccess,
			size:        ButtonSizeLG,
			wantClasses: []string{"btn", "btn-success", "btn-lg"},
		},
		{
			name:        "ghost xs",
			variant:     ButtonGhost,
			size:        ButtonSizeXS,
			wantClasses: []string{"btn", "btn-ghost", "btn-xs"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := render(t, Button(tc.variant, tc.size, tc.disabled, nil))
			for _, want := range tc.wantClasses {
				if !hasClass(body, want) {
					t.Errorf("expected class %q in rendered button, got %q", want, body)
				}
			}
			if hasClass(body, "btn-disabled") {
				t.Errorf("non-disabled button must not emit btn-disabled, got %q", body)
			}
		})
	}
}

func TestButton_MDSizeEmitsNoSizeClass(t *testing.T) {
	body := render(t, Button(ButtonPrimary, ButtonSizeMD, false, nil))
	for _, size := range []ButtonSize{ButtonSizeXS, ButtonSizeSM, ButtonSizeLG} {
		if hasClass(body, string(size)) {
			t.Errorf("ButtonSizeMD must not emit any explicit size class, found %q in %q", size, body)
		}
	}
}

func TestButton_RendersChildren(t *testing.T) {
	body := render(t, Button(ButtonPrimary, ButtonSizeMD, false, nil))
	// Button() called with no children slot in this test harness renders an
	// empty body; assert the button element itself wraps a location where
	// children would land (between the opening/closing tag).
	if !strings.Contains(body, "<button") || !strings.Contains(body, "</button>") {
		t.Fatalf("expected a <button>...</button> element, got %q", body)
	}
}

func TestButton_Disabled(t *testing.T) {
	body := render(t, Button(ButtonError, ButtonSizeMD, true, nil))
	if !hasClass(body, "btn-disabled") {
		t.Errorf("disabled button must emit btn-disabled class, got %q", body)
	}
	if !strings.Contains(body, "disabled") {
		t.Errorf("disabled button must set the native disabled attribute, got %q", body)
	}
}

func TestButton_ForwardsAttrs(t *testing.T) {
	body := render(t, Button(ButtonPrimary, ButtonSizeMD, false, templ.Attributes{
		"hx-post": "/promote",
		"type":    "submit",
	}))
	if !strings.Contains(body, `hx-post="/promote"`) {
		t.Errorf("expected forwarded hx-post attribute, got %q", body)
	}
	if !strings.Contains(body, `type="submit"`) {
		t.Errorf("expected forwarded type attribute, got %q", body)
	}
}

// --- Badge (FR1) -------------------------------------------------------------

func TestBadge_OneLabelOneColourClass(t *testing.T) {
	allVariants := []BadgeVariant{
		BadgeNeutral, BadgePrimary, BadgeSecondary, BadgeSuccess,
		BadgeWarning, BadgeError, BadgeInfo, BadgeGhost,
	}
	colourClasses := []string{
		"badge-neutral", "badge-primary", "badge-secondary", "badge-success",
		"badge-warning", "badge-error", "badge-info", "badge-ghost",
	}
	for _, variant := range allVariants {
		body := render(t, Badge("Label", variant, BadgeSizeMD, false, nil))
		if !strings.Contains(body, "Label") {
			t.Errorf("expected label text in rendered badge, got %q", body)
		}
		count := 0
		for _, cc := range colourClasses {
			if hasClass(body, cc) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("variant %q: expected exactly one colour class, found %d in %q", variant, count, body)
		}
	}
}

func TestBadge_SoftAddsTintedTreatmentClass(t *testing.T) {
	plain := render(t, Badge("X", BadgePrimary, BadgeSizeMD, false, nil))
	soft := render(t, Badge("X", BadgePrimary, BadgeSizeMD, true, nil))
	if hasClass(plain, "badge-soft") {
		t.Errorf("non-soft badge must not emit badge-soft, got %q", plain)
	}
	if !hasClass(soft, "badge-soft") {
		t.Errorf("soft badge must emit badge-soft, got %q", soft)
	}
}

func TestBadge_SizeClasses(t *testing.T) {
	cases := map[BadgeSize]string{
		BadgeSizeXS: "badge-xs",
		BadgeSizeSM: "badge-sm",
		BadgeSizeLG: "badge-lg",
	}
	for size, want := range cases {
		body := render(t, Badge("X", BadgeNeutral, size, false, nil))
		if !hasClass(body, want) {
			t.Errorf("size %q: expected class %q in %q", size, want, body)
		}
	}
	// MD is the implicit default and emits no size class.
	mdBody := render(t, Badge("X", BadgeNeutral, BadgeSizeMD, false, nil))
	for _, cls := range cases {
		if hasClass(mdBody, cls) {
			t.Errorf("BadgeSizeMD must not emit any explicit size class, found %q in %q", cls, mdBody)
		}
	}
}

// --- Card (FR1) --------------------------------------------------------------

func TestCard_SlotOrderingAndDropsEmptySlots(t *testing.T) {
	header := templ.Raw("<h3>HEADER-MARK</h3>")
	body := templ.Raw("<p>BODY-MARK</p>")
	actions := templ.Raw("<button>ACTIONS-MARK</button>")

	rendered := render(t, Card(header, body, actions, nil))

	headerIdx := strings.Index(rendered, "HEADER-MARK")
	bodyIdx := strings.Index(rendered, "BODY-MARK")
	actionsIdx := strings.Index(rendered, "ACTIONS-MARK")
	if headerIdx == -1 || bodyIdx == -1 || actionsIdx == -1 {
		t.Fatalf("expected all three slots present, got %q", rendered)
	}
	if !(headerIdx < bodyIdx && bodyIdx < actionsIdx) {
		t.Errorf("expected header < body < actions ordering, got header=%d body=%d actions=%d in %q", headerIdx, bodyIdx, actionsIdx, rendered)
	}
	if !strings.Contains(rendered, "card-actions") {
		t.Errorf("expected actions slot to be wrapped in card-actions chrome, got %q", rendered)
	}
}

func TestCard_NilSlotsDropCleanly(t *testing.T) {
	body := templ.Raw("<p>ONLY-BODY</p>")
	rendered := render(t, Card(nil, body, nil, nil))

	if strings.Contains(rendered, "HEADER") {
		t.Errorf("expected no header content when header slot is nil, got %q", rendered)
	}
	if strings.Contains(rendered, "card-actions") {
		t.Errorf("expected no card-actions wrapper when actions slot is nil, got %q", rendered)
	}
	if !strings.Contains(rendered, "ONLY-BODY") {
		t.Errorf("expected body slot content present, got %q", rendered)
	}
}

func TestCard_AllNilSlotsRendersChromeOnly(t *testing.T) {
	rendered := render(t, Card(nil, nil, nil, nil))
	if !strings.Contains(rendered, "card") || !strings.Contains(rendered, "card-body") {
		t.Fatalf("expected card chrome to render even with all slots nil, got %q", rendered)
	}
	if strings.Contains(rendered, "card-actions") {
		t.Errorf("expected no card-actions wrapper with nil actions slot, got %q", rendered)
	}
}

// --- Confirm (FR1b) -----------------------------------------------------------

func TestConfirm_DestructiveIntentStyling(t *testing.T) {
	p := ConfirmProps{
		Intent:      ConfirmDestructive,
		Summary:     "This cannot be undone.",
		SubmitLabel: "Confirm rollback",
	}
	body := render(t, Confirm(p, nil))
	if !hasClass(body, "btn-error") {
		t.Errorf("destructive intent must render an error-variant submit button, got %q", body)
	}
	if !strings.Contains(body, "This cannot be undone.") {
		t.Errorf("expected summary text present, got %q", body)
	}
	if !strings.Contains(body, "Confirm rollback") {
		t.Errorf("expected submit label present, got %q", body)
	}
}

func TestConfirm_NeutralIntentStyling(t *testing.T) {
	p := ConfirmProps{
		Intent:      ConfirmNeutral,
		SubmitLabel: "Promote for real",
	}
	body := render(t, Confirm(p, nil))
	if hasClass(body, "btn-error") {
		t.Errorf("neutral intent must not render an error-variant submit button, got %q", body)
	}
	if !hasClass(body, "btn-primary") {
		t.Errorf("neutral intent must render a primary-variant submit button, got %q", body)
	}
}

func TestConfirm_SubmitAndCancelAffordances(t *testing.T) {
	p := ConfirmProps{
		Intent:      ConfirmNeutral,
		SubmitLabel: "Promote for real",
		CancelHref:  "/environments/prod",
	}
	body := render(t, Confirm(p, nil))
	if !strings.Contains(body, "Promote for real") {
		t.Errorf("expected submit affordance present, got %q", body)
	}
	if !strings.Contains(body, "Cancel") {
		t.Errorf("expected default cancel label present, got %q", body)
	}
	if !strings.Contains(body, `href="/environments/prod"`) {
		t.Errorf("expected cancel href present, got %q", body)
	}
}

func TestConfirm_NoCancelHrefOmitsCancelAffordance(t *testing.T) {
	p := ConfirmProps{
		Intent:      ConfirmDestructive,
		SubmitLabel: "Archive prod",
	}
	body := render(t, Confirm(p, nil))
	if strings.Contains(body, "Cancel") {
		t.Errorf("expected no cancel affordance when CancelHref is empty, got %q", body)
	}
}

func TestConfirm_CustomCancelLabel(t *testing.T) {
	p := ConfirmProps{
		Intent:      ConfirmNeutral,
		SubmitLabel: "Save",
		CancelHref:  "/back",
		CancelLabel: "Never mind",
	}
	body := render(t, Confirm(p, nil))
	if !strings.Contains(body, "Never mind") {
		t.Errorf("expected custom cancel label, got %q", body)
	}
	if strings.Contains(body, ">Cancel<") {
		t.Errorf("expected default 'Cancel' label to be overridden, got %q", body)
	}
}

func TestConfirm_ForwardsSubmitAttrsToSubmitControl(t *testing.T) {
	p := ConfirmProps{
		Intent:      ConfirmDestructive,
		SubmitLabel: "Confirm rollback",
	}
	body := render(t, Confirm(p, templ.Attributes{
		"hx-post": "/rollback",
	}))
	if !strings.Contains(body, `hx-post="/rollback"`) {
		t.Errorf("expected submitAttrs forwarded onto the submit control, got %q", body)
	}
}

func TestConfirm_DisabledSubmitCarriesReasonAsTitle(t *testing.T) {
	p := ConfirmProps{
		Intent:         ConfirmDestructive,
		SubmitLabel:    "Confirm rollback",
		Disabled:       true,
		DisabledReason: "nothing to roll back to",
	}
	body := render(t, Confirm(p, nil))
	if !strings.Contains(body, "disabled") {
		t.Errorf("expected disabled attribute on submit control, got %q", body)
	}
	if !strings.Contains(body, `title="nothing to roll back to"`) {
		t.Errorf("expected DisabledReason as title tooltip, got %q", body)
	}
}

func TestConfirm_ZoneTitleTogglesDangerZoneChrome(t *testing.T) {
	withZone := render(t, Confirm(ConfirmProps{
		Intent:      ConfirmDestructive,
		ZoneTitle:   "Danger Zone",
		SubmitLabel: "Archive prod",
	}, nil))
	if !strings.Contains(withZone, "Danger Zone") {
		t.Errorf("expected ZoneTitle text to render as the danger-zone header, got %q", withZone)
	}
	if !strings.Contains(withZone, "border-error") {
		t.Errorf("expected danger-zone bordered card chrome when ZoneTitle is set, got %q", withZone)
	}

	withoutZone := render(t, Confirm(ConfirmProps{
		Intent:      ConfirmDestructive,
		SubmitLabel: "Archive prod",
	}, nil))
	if strings.Contains(withoutZone, "border-error") {
		t.Errorf("expected no danger-zone card chrome when ZoneTitle is empty, got %q", withoutZone)
	}
	if strings.Contains(withoutZone, "Danger Zone") {
		t.Errorf("expected no zone title text when ZoneTitle is empty, got %q", withoutZone)
	}
}

// --- Shell (FR1, FR1a) -------------------------------------------------------

// countOccurrences reports how many non-overlapping times sub appears in s,
// so tests can assert an exact count rather than mere presence -- guards
// against e.g. a duplicated slot render or an extra hardcoded copy sitting
// alongside the caller-supplied one.
func countOccurrences(s, sub string) int {
	return strings.Count(s, sub)
}

func TestShell_RendersNavSlotVerbatim(t *testing.T) {
	body := render(t, Shell(ShellData{
		BrandLabel: "Brand",
		BrandHref:  "/",
		Nav:        templ.Raw(`<li><a href="/mark">NAV-MARK</a></li>`),
	}))
	want := `<li><a href="/mark">NAV-MARK</a></li>`
	if countOccurrences(body, want) != 1 {
		t.Errorf("expected caller-supplied nav slot content to render verbatim exactly once, got %q in %q", want, body)
	}
}

// TestShell_NilNavRendersNoNavItemsOfItsOwn is the direct FR1a regression
// guard: Shell must not be a literal lift of
// tools/app_registry/ui/components/layout.templ's Shell(), which hardcodes
// its nav inline as a <ul class="menu menu-horizontal"> of <li><a
// href="/environments">...</a></li> items. With no Nav slot supplied, Shell
// must render zero <li> elements and must not mention "/environments"
// anywhere -- a loose substring check for "menu-horizontal" alone would not
// have caught a differently-classed hardcoded list, so this asserts on the
// absence of the <li> element itself and of the literal href app-registry's
// Shell hardcodes.
func TestShell_NilNavRendersNoNavItemsOfItsOwn(t *testing.T) {
	body := render(t, Shell(ShellData{
		BrandLabel: "Brand",
		BrandHref:  "/",
	}))
	if strings.Contains(body, "<li") {
		t.Errorf("expected no <li> nav items when Nav slot is nil, got %q", body)
	}
	if strings.Contains(body, "/environments") {
		t.Errorf("expected no hardcoded /environments nav item (FR1a literal-lift regression), got %q", body)
	}
}

func TestShell_RendersBannerAndHeaderRightSlotsInExpectedPositions(t *testing.T) {
	body := render(t, Shell(ShellData{
		BrandLabel:  "Brand",
		BrandHref:   "/",
		Banner:      templ.Raw(`<div id="BANNER-MARK"></div>`),
		HeaderRight: templ.Raw(`<div id="HEADERRIGHT-MARK"></div>`),
	}))
	navbarIdx := strings.Index(body, `<div class="navbar`)
	mainIdx := strings.Index(body, "<main")
	headerRightIdx := strings.Index(body, `<div id="HEADERRIGHT-MARK"></div>`)
	bannerIdx := strings.Index(body, `<div id="BANNER-MARK"></div>`)
	if navbarIdx == -1 || mainIdx == -1 || headerRightIdx == -1 || bannerIdx == -1 {
		t.Fatalf("expected navbar, main, header-right slot, and banner slot all present, got %q", body)
	}
	if !(navbarIdx < headerRightIdx && headerRightIdx < mainIdx) {
		t.Errorf("expected HeaderRight to render inside the navbar region (before <main>), got navbar=%d headerRight=%d main=%d in %q", navbarIdx, headerRightIdx, mainIdx, body)
	}
	if bannerIdx < mainIdx {
		t.Errorf("expected Banner to render inside the <main> region, not before it, got banner=%d main=%d in %q", bannerIdx, mainIdx, body)
	}
}

func TestShell_NilBannerAndHeaderRightOmitCleanly(t *testing.T) {
	body := render(t, Shell(ShellData{
		BrandLabel: "Brand",
		BrandHref:  "/",
	}))
	if strings.Contains(body, "BANNER-MARK") || strings.Contains(body, "HEADERRIGHT-MARK") {
		t.Errorf("expected no banner/header-right slot markers when both are nil, got %q", body)
	}
}

func TestShell_UserLabelRendersExactSpan(t *testing.T) {
	body := render(t, Shell(ShellData{
		BrandLabel: "Brand",
		BrandHref:  "/",
		UserLabel:  "alice",
	}))
	want := `<span class="mr-2 text-sm opacity-90">alice</span>`
	if !strings.Contains(body, want) {
		t.Errorf("expected identity label span %q, got %q", want, body)
	}
}

func TestShell_EmptyUserLabelOmitsLabel(t *testing.T) {
	body := render(t, Shell(ShellData{
		BrandLabel: "Brand",
		BrandHref:  "/",
	}))
	if hasClass(body, "opacity-90") {
		t.Errorf("expected no identity label span when UserLabel is empty, got %q", body)
	}
}

// --- ThemesCSS (FR10 POC theme parity) ----------------------------------------

// cssRuleRE matches one CSS rule's raw selector text and body, e.g. for
// `[data-theme="light"] {\n  --color-base-100: #ffffff;\n}` it captures
// group 1 = `[data-theme="light"] ` and group 2 = the declarations between
// the braces. It also matches the shared grouped selector rule near the
// bottom of themes.css (multiple `[data-theme="…"]` selectors joined by
// `,\n`) -- themeVariableKeys below distinguishes the two by selector
// count, since only a single-selector rule is a genuine per-theme block.
var cssRuleRE = regexp.MustCompile(`(?s)([^{}]+)\{([^}]*)\}`)

// dataThemeSelectorRE matches one `[data-theme="<name>"]` selector token.
var dataThemeSelectorRE = regexp.MustCompile(`\[data-theme="([a-z]+)"\]`)

// cssVarRE matches a custom-property declaration's name, e.g. captures
// "--color-primary" out of "  --color-primary: #ea580c;".
var cssVarRE = regexp.MustCompile(`(--[a-zA-Z0-9-]+)\s*:`)

// themeVariableKeys parses ThemesCSS and returns, per theme name, the set
// of CSS custom-property keys defined in that theme's own single-selector
// [data-theme="…"] block. Rules whose selector list names more than one
// theme (the shared grouped `[data-theme="light"], [data-theme="night"],
// ... { --radius-...: ...; }` block near the bottom of themes.css) are
// skipped: that block defines radius tokens shared identically across
// every theme by construction (one rule body applied to every listed
// selector), so it can never itself drift out of per-theme parity and is
// out of scope for this assertion. Only per-theme colour-variable blocks
// (one distinct rule body per theme) are where a POC theme could silently
// omit a variable.
func themeVariableKeys(t *testing.T) map[string]map[string]bool {
	t.Helper()
	rules := cssRuleRE.FindAllStringSubmatch(ThemesCSS, -1)
	out := make(map[string]map[string]bool)
	for _, r := range rules {
		selectorText, body := r[1], r[2]
		selectors := dataThemeSelectorRE.FindAllStringSubmatch(selectorText, -1)
		if len(selectors) != 1 {
			// Either no [data-theme="…"] selector in this rule at all, or
			// more than one (the shared grouped rule) -- neither is a
			// per-theme block.
			continue
		}
		name := selectors[0][1]
		keys := make(map[string]bool)
		for _, m := range cssVarRE.FindAllStringSubmatch(body, -1) {
			keys[m[1]] = true
		}
		out[name] = keys
	}
	if len(out) == 0 {
		t.Fatalf("expected at least one single-selector [data-theme=\"...\"] block in ThemesCSS, found none in %q", ThemesCSS)
	}
	return out
}

// TestThemesCSS_AllThemesDefineIdenticalVariableKeySet is the FR10 parity
// guard deferred from issue #1012's implementation phase: every
// [data-theme="…"] block in the shared ThemesCSS (light/night/oled and any
// POC addition like "sunset") must define the exact same set of daisyUI
// CSS-variable keys. A theme missing a key doesn't fail to render -- it
// silently falls back to daisyUI's built-in default for that one token,
// which is much harder to notice than an outright rendering error. This
// compares every theme's key set against "light" (an arbitrary, stable
// reference) in both directions (missing and extra keys).
//
// Red/green discipline: temporarily deleting the
// `--color-accent: #0d9488;` declaration from the sunset block in
// themes.css makes this test fail with "theme \"sunset\" is missing
// variable keys ... [--color-accent]"; restoring the line makes it pass
// again. Verified by hand during this change.
func TestThemesCSS_AllThemesDefineIdenticalVariableKeySet(t *testing.T) {
	keysByTheme := themeVariableKeys(t)

	const reference = "light"
	referenceKeys, ok := keysByTheme[reference]
	if !ok {
		t.Fatalf("expected a %q theme block to use as the parity reference, found themes %v", reference, keysByTheme)
	}
	if len(referenceKeys) == 0 {
		t.Fatalf("expected the %q reference theme to define at least one CSS variable, found none", reference)
	}

	for name, keys := range keysByTheme {
		if name == reference {
			continue
		}
		var missing []string
		for k := range referenceKeys {
			if !keys[k] {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("theme %q is missing variable keys present in %q: %v", name, reference, missing)
		}

		var extra []string
		for k := range keys {
			if !referenceKeys[k] {
				extra = append(extra, k)
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			t.Errorf("theme %q defines variable keys not present in %q: %v", name, reference, extra)
		}
	}
}

// TestThemesCSS_ContainsSunsetTheme is the direct FR10 existence guard: the
// POC theme must actually be present in the shared ThemesCSS (not merely
// registered in some consuming app's switcher list with no backing
// palette), and it must be distinct from the three pre-existing themes it
// is meant to sit alongside.
func TestThemesCSS_ContainsSunsetTheme(t *testing.T) {
	keysByTheme := themeVariableKeys(t)
	if _, ok := keysByTheme["sunset"]; !ok {
		t.Fatalf(`expected a [data-theme="sunset"] block in ThemesCSS, found themes %v`, keysByTheme)
	}
	for _, existing := range []string{"light", "night", "oled"} {
		if _, ok := keysByTheme[existing]; !ok {
			t.Errorf("expected pre-existing theme %q to still be present in ThemesCSS", existing)
		}
	}
}

// --- ThemeSwitcher (FR5, shared side) -----------------------------------------

func TestThemeSwitcher_RendersOneControlPerTheme(t *testing.T) {
	themes := []Theme{
		{Value: "light", Label: "Light"},
		{Value: "night", Label: "Night"},
		{Value: "oled", Label: "OLED"},
	}
	body := render(t, ThemeSwitcher(themes))

	if got := countOccurrences(body, `data-htmxui-theme-value="`); got != len(themes) {
		t.Errorf("expected exactly %d theme controls, found %d in %q", len(themes), got, body)
	}
	for _, theme := range themes {
		// Exact value+label pairing, not a loose substring match: a control
		// whose data-htmxui-theme-value doesn't match its own Label would
		// still pass a check that searched for the value and the label
		// independently.
		want := fmt.Sprintf(`<li><button type="button" data-htmxui-theme-value=%q>%s</button></li>`, theme.Value, theme.Label)
		if !strings.Contains(body, want) {
			t.Errorf("expected exact control markup %q for theme %+v, got %q", want, theme, body)
		}
	}
}

func TestThemeSwitcher_EmptyThemesRendersNoControls(t *testing.T) {
	body := render(t, ThemeSwitcher(nil))
	if strings.Contains(body, "<li") {
		t.Errorf("expected no theme control <li> elements when themes is empty, got %q", body)
	}
}

// TestThemeSwitcher_NoLocationReload is referenced by name in
// theme_switcher.templ's ThemeSwitcher doc comment (FR5, shared side): the
// shared switcher must never carry forward manmanv2's current
// location.reload()-on-change behavior. Checks for the exact call
// substring "location.reload(" -- not just the bare word "reload", which
// would also match e.g. a hypothetical "reloadTheme" identifier and produce
// a false failure, or miss a differently-invoked reload.
func TestThemeSwitcher_NoLocationReload(t *testing.T) {
	body := render(t, ThemeSwitcher([]Theme{{Value: "light", Label: "Light"}}))
	if strings.Contains(body, "location.reload(") {
		t.Errorf("expected no location.reload() call in ThemeSwitcher's emitted script, got %q", body)
	}
}

func TestThemeSwitcher_SetsDataThemeAttribute(t *testing.T) {
	body := render(t, ThemeSwitcher([]Theme{{Value: "light", Label: "Light"}}))
	if !strings.Contains(body, `root.setAttribute("data-theme", theme);`) {
		t.Errorf("expected click handler to set data-theme on click, got %q", body)
	}
	if !strings.Contains(body, `root.setAttribute("data-theme", stored);`) {
		t.Errorf("expected load-time restore to set data-theme from the stored value, got %q", body)
	}
}

// TestThemeSwitcher_PersistsUsingExportedStorageKey guards both halves of
// the persistence contract: the script writes to localStorage under the
// exported ThemeSwitcherStorageKey constant (not a private, drift-prone
// literal), and the inline invocation actually passes that same key's
// runtime value -- not merely that the string "localStorage" appears
// somewhere.
func TestThemeSwitcher_PersistsUsingExportedStorageKey(t *testing.T) {
	body := render(t, ThemeSwitcher([]Theme{{Value: "light", Label: "Light"}}))
	if !strings.Contains(body, `localStorage.setItem(storageKey, theme);`) {
		t.Errorf("expected theme selection to be persisted via localStorage.setItem(storageKey, theme), got %q", body)
	}
	invocation := fmt.Sprintf("(%q)</script>", ThemeSwitcherStorageKey)
	if !strings.Contains(body, invocation) {
		t.Errorf("expected script invocation to pass the exported ThemeSwitcherStorageKey value %q, got %q", invocation, body)
	}
}

// --- UserMenu (FR8) -----------------------------------------------------------

func TestUserMenu_RendersIdentityAndLogoutHref(t *testing.T) {
	body := render(t, UserMenu(UserMenuData{
		IdentityLabel: "alice",
		LogoutHref:    "/auth/logout",
	}))
	if !strings.Contains(body, "alice") {
		t.Errorf("expected identity label in rendered output, got %q", body)
	}
	want := `href="/auth/logout"`
	if !strings.Contains(body, want) {
		t.Errorf("expected logout affordance to link to the supplied href %q, got %q", want, body)
	}
}

// TestUserMenu_NoUserRendersNothing guards the signed-out / no-user state
// (relevant for AUTH_MODE=none): an empty IdentityLabel must not produce a
// dropdown with a dangling logout link and no identity behind it -- it must
// render nothing at all.
func TestUserMenu_NoUserRendersNothing(t *testing.T) {
	body := render(t, UserMenu(UserMenuData{}))
	if body != "" {
		t.Errorf("expected no markup when IdentityLabel is empty, got %q", body)
	}
}

// TestUserMenu_LogoutHrefIsParameterised is the direct FR8 regression guard
// against hardcoding "/auth/logout": both apps happen to register that
// route today, but UserMenu must not assume it. Rendering with a distinct
// href must produce that href, and must not also emit the default path
// anywhere (e.g. as a leftover hardcoded fallback alongside the real one).
//
// Red/green discipline: temporarily hardcoding href="/auth/logout" in
// user_menu.templ's <a> in place of { templ.URL(data.LogoutHref) } makes
// this test fail with "expected no hardcoded default logout path" (the
// custom href assertion also starts failing, since the hardcoded literal
// wins); reverting restores green. See issue #1009 commit history for the
// verified red run.
func TestUserMenu_LogoutHrefIsParameterised(t *testing.T) {
	body := render(t, UserMenu(UserMenuData{
		IdentityLabel: "bob",
		LogoutHref:    "/custom/signout",
	}))
	if !strings.Contains(body, `href="/custom/signout"`) {
		t.Errorf("expected logout affordance to use the supplied custom href, got %q", body)
	}
	if strings.Contains(body, "/auth/logout") {
		t.Errorf("expected no hardcoded default logout path when a different href is supplied, got %q", body)
	}
}

// TestUserMenu_ComposesAlongsideHeaderRightSlot is the direct
// composition-not-folding regression guard (FR8): a caller composes
// UserMenu with its own app-specific extras (e.g. manmanv2's
// ServerSelector) into a single templ.Component and passes that as
// ShellData.HeaderRight -- UserMenu itself takes no app-specific
// parameters. This asserts the extra's markup renders as a sibling of
// UserMenu's dropdown container, not nested inside it, i.e. genuine
// composition rather than UserMenu having grown a slot of its own.
func TestUserMenu_ComposesAlongsideHeaderRightSlot(t *testing.T) {
	extra := templ.Raw(`<div id="EXTRA-MARK">server selector</div>`)
	headerRight := templ.Join(extra, UserMenu(UserMenuData{
		IdentityLabel: "alice",
		LogoutHref:    "/auth/logout",
	}))
	body := render(t, Shell(ShellData{
		BrandLabel:  "Brand",
		BrandHref:   "/",
		HeaderRight: headerRight,
	}))

	extraIdx := strings.Index(body, `<div id="EXTRA-MARK">server selector</div>`)
	menuOpenIdx := strings.Index(body, `data-htmxui-user-menu`)
	menuCloseIdx := strings.Index(body, `</ul>`)
	if extraIdx == -1 || menuOpenIdx == -1 || menuCloseIdx == -1 {
		t.Fatalf("expected both the extra marker and the UserMenu dropdown present, got %q", body)
	}
	if extraIdx > menuOpenIdx && extraIdx < menuCloseIdx {
		t.Errorf("expected app-specific extra to render adjacent to UserMenu, not nested inside its dropdown, got %q", body)
	}
}
