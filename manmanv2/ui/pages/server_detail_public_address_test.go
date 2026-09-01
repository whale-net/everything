package pages

import (
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
)

// TestServerDetail_PublicAddressSetRendersValueAndPrefillsForm guards #1528
// (FR4): when a server's HostPublicAddress is populated, the value must be
// visible in the "Server Information" card and pre-filled into the
// host_public_address edit-form input -- not just one or the other, since
// an admin editing the form needs to see the current value to know what
// they're changing.
func TestServerDetail_PublicAddressSetRendersValueAndPrefillsForm(t *testing.T) {
	server := &manmanpb.Server{
		ServerId:          5,
		Name:              "Gamma",
		Status:            "online",
		HostPublicAddress: "game.example.com:27015",
	}
	body := renderPage(t, ServerDetail(components.LayoutData{Title: "Server"}, server, nil))

	if !strings.Contains(body, "game.example.com:27015") {
		t.Errorf("expected the current public address to render in the page body, got %q", body)
	}
	if !strings.Contains(body, `value="game.example.com:27015"`) {
		t.Errorf("expected the host_public_address input to be pre-filled with the current value, got %q", body)
	}
	if strings.Contains(body, "Not set") {
		t.Errorf("expected no \"Not set\" placeholder when HostPublicAddress is populated, got %q", body)
	}
}

// TestServerDetail_PublicAddressUnsetRendersPlaceholderAndEmptyInput
// guards the complementary #1528 case: an unset address (empty string,
// the zero value the field arrives with before #1527's migration is ever
// exercised) must render a "Not set" placeholder and still present the
// edit input, empty, so an admin can populate it for the first time.
func TestServerDetail_PublicAddressUnsetRendersPlaceholderAndEmptyInput(t *testing.T) {
	server := &manmanpb.Server{
		ServerId:          6,
		Name:              "Delta",
		Status:            "offline",
		HostPublicAddress: "",
	}
	body := renderPage(t, ServerDetail(components.LayoutData{Title: "Server"}, server, nil))

	if !strings.Contains(body, "Not set") {
		t.Errorf("expected a \"Not set\" placeholder when HostPublicAddress is empty, got %q", body)
	}
	if !strings.Contains(body, `id="host_public_address"`) {
		t.Errorf("expected the host_public_address input to still be present when unset, got %q", body)
	}
	if !strings.Contains(body, `value=""`) {
		t.Errorf("expected the host_public_address input to be empty (not stale/prefilled) when unset, got %q", body)
	}

	// The edit form itself must post to this server's update-address route
	// regardless of current value, and stay a POST (no anonymous GET/link
	// affordance introduced -- NFR2/FR10 is enforced server-side by the
	// handler, but the form must actually target that route).
	if !strings.Contains(body, `action="/servers/6/update-address"`) {
		t.Errorf("expected the edit form to POST to /servers/6/update-address, got %q", body)
	}
	if !strings.Contains(body, `method="POST"`) {
		t.Errorf("expected the edit form to use POST, got %q", body)
	}
}
