package pages

import (
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
)

// Guards task #2091 (FR6/FR7, plan #2080): the reworked Actions management
// page must keep the edit round-trip entry points and the select/radio
// option editor intact, and its submit path must still carry input_options
// (the legacy page's verified failure mode was submitting an empty
// input_options array, silently wiping option lists on update).

func actionsManageTestData() ActionsManageData {
	localAction := &manmanpb.ActionDefinition{
		ActionId:        42,
		DefinitionLevel: "game",
		EntityId:        1,
		Name:            "change_map",
		Label:           "Change Map",
		Enabled:         true,
		InputFields: []*manmanpb.ActionInputField{
			{
				FieldId:   7,
				Name:      "map",
				Label:     "Select Map",
				FieldType: "select",
				Required:  true,
				Options: []*manmanpb.ActionInputOption{
					{OptionId: 1, FieldId: 7, Value: "de_dust2", Label: "Dust II", IsDefault: true},
					{OptionId: 2, FieldId: 7, Value: "de_mirage", Label: "Mirage"},
				},
			},
		},
	}
	return ActionsManageData{
		DefinitionLevel: "game",
		EntityID:        1,
		CurrentPath:     "/games/1/actions",
		LocalActions: []*ActionManageRow{
			{Action: localAction, EditURL: ""},
		},
		InheritedActions: []*ActionManageRow{
			{
				Action:  &manmanpb.ActionDefinition{ActionId: 9, Name: "inherited_one", Label: "Inherited One"},
				EditURL: "/games/5/actions",
			},
		},
		FieldTypes:    []string{"text", "select"},
		ButtonStyles:  []string{"primary", "secondary"},
		IconOptions:   []ActionIconOption{{Class: "fa-map", Label: "Map"}},
	}
}

func TestActionsManage_EditRoundTripWiring(t *testing.T) {
	body := renderPage(t, ActionsManage(components.LayoutData{Title: "Manage Actions"}, actionsManageTestData()))

	// Local action rows expose the hx-get edit entry point against the page's
	// current path -- the request handleActionEditForm answers with the
	// action JSON (fields AND options) that populateEditForm consumes.
	if !strings.Contains(body, `hx-get="/games/1/actions/edit/42"`) {
		t.Errorf("expected local action edit button wired to /games/1/actions/edit/42, got %q", body)
	}

	// Inherited actions edit at their definition level.
	if !strings.Contains(body, `href="/games/5/actions"`) {
		t.Errorf("expected inherited action's Edit-at-source link to /games/5/actions, got %q", body)
	}

	// The Alpine edit handler loads existing fields including option lists
	// (field.options -> _options), so an edit opens pre-populated options
	// rather than an empty list.
	if !strings.Contains(body, "(field.options || [])") {
		t.Errorf("expected populateEditForm to load field.options, got %q", body)
	}

	// The submit path sends every field's options with the field's position
	// as the linking field_id -- the create/update payload contract the API
	// and repository rely on for option persistence.
	if !strings.Contains(body, "input_options") || !strings.Contains(body, "field_id: fieldIndex") {
		t.Errorf("expected submit to send input_options keyed by field index, got %q", body)
	}

	// The select/radio option editor is present (add/remove option rows).
	if !strings.Contains(body, "getOptionsForField") || !strings.Contains(body, "addOption") || !strings.Contains(body, "removeOption") {
		t.Errorf("expected option editor wiring (getOptionsForField/addOption/removeOption), got %q", body)
	}
}

func TestActionsManage_RendersLocalAndInheritedRows(t *testing.T) {
	body := renderPage(t, ActionsManage(components.LayoutData{Title: "Manage Actions"}, actionsManageTestData()))

	for _, want := range []string{"Change Map", "Inherited One"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected page to render action label %q, got %q", want, body)
		}
	}
}
