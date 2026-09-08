package pages

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/manmanv2/ui/components"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// Guards task #2095's allowed host-port ranges section (FR12): rows render
// with remove/edit affordances, the zero-ranges state nudges toward
// configuring ranges while stating assignment stays unconstrained
// (SB-1.2), and edit mode swaps the row for a form.

func renderPortRangesSection(t *testing.T, server *manmanpb.Server, notice string, edit *manmanpb.PortRange) string {
	t.Helper()
	return renderPage(t, ServerDetail(components.LayoutData{Title: "Server"}, server, nil, notice, edit))
}

func TestServerPortRangesSection_RendersRowsAndForms(t *testing.T) {
	server := &manmanpb.Server{
		ServerId: 2,
		Name:     "srv-2",
		Status:   "online",
		AllowedPortRanges: []*manmanpb.PortRange{
			{Start: 25565, End: 25570, Protocol: "TCP"},
			{Start: 27015, End: 27020, Protocol: "UDP"},
		},
	}
	body := renderPortRangesSection(t, server, "", nil)

	for _, want := range []string{
		"Allowed Host Port Ranges",
		"25565-25570",
		"27015-27020",
		"/servers/2/ports/remove",
		"/servers/2/ports/set",
		"/servers/2/ports/edit?start=25565&amp;end=25570&amp;protocol=TCP",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q", want)
		}
	}
	// The nudge must NOT show when ranges exist.
	if strings.Contains(body, "assignment is currently unconstrained") {
		t.Errorf("empty-state nudge rendered even though ranges exist")
	}
}

func TestServerPortRangesSection_EmptyStateNudgesAndStaysUnconstrained(t *testing.T) {
	server := &manmanpb.Server{ServerId: 2, Name: "srv-2", Status: "online"}
	body := renderPortRangesSection(t, server, "", nil)

	for _, want := range []string{
		"No allowed host-port ranges configured",
		"unconstrained",
		"Add Range",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q", want)
		}
	}
}

func TestServerPortRangesSection_EditModeSwapsRowForForm(t *testing.T) {
	server := &manmanpb.Server{
		ServerId: 2,
		Name:     "srv-2",
		Status:   "online",
		AllowedPortRanges: []*manmanpb.PortRange{
			{Start: 25565, End: 25570, Protocol: "TCP"},
			{Start: 27015, End: 27020, Protocol: "UDP"},
		},
	}
	body := renderPortRangesSection(t, server, "", &manmanpb.PortRange{Start: 25565, End: 25570, Protocol: "TCP"})

	// The edited row renders inputs bound to the single edit form.
	if !strings.Contains(body, `form="port-range-edit-2-25565-25570-TCP"`) {
		t.Errorf("expected edit-form inputs bound to the edit form id")
	}
	if !strings.Contains(body, `name="orig_start"`) {
		t.Errorf("expected original identity hidden fields")
	}
	// Untouched rows keep their normal remove form; the untouched edit link for the edited row is gone.
	if strings.Contains(body, "/servers/2/ports/edit?start=25565&amp;end=25570&amp;protocol=TCP") {
		t.Errorf("edited row still rendered its static row")
	}
	if !strings.Contains(body, "/servers/2/ports/edit?start=27015&amp;end=27020&amp;protocol=UDP") {
		t.Errorf("untouched row lost its edit link")
	}
	// The add form is hidden while editing.
	if strings.Contains(body, ">Add Range</button>") {
		t.Errorf("add form rendered while a row is in edit mode")
	}
}
