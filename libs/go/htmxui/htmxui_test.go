package htmxui

import (
	"context"
	"io"
	"regexp"
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
