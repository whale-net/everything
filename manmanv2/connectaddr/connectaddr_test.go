package connectaddr

import (
	"reflect"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

func TestDerive(t *testing.T) {
	one := []*manmanpb.PortBinding{{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"}}
	multi := []*manmanpb.PortBinding{
		{HostPort: 25565, Protocol: "TCP"},
		{HostPort: 19132, Protocol: "UDP"},
	}
	cases := []struct {
		name string
		host string
		pbs  []*manmanpb.PortBinding
		want Result
	}{
		{"single", "play.example.com", one, Result{Entries: []Entry{{"play.example.com", 25565, "TCP"}}}},
		{"multi", "1.2.3.4", multi, Result{Entries: []Entry{{"1.2.3.4", 25565, "TCP"}, {"1.2.3.4", 19132, "UDP"}}}},
		{"no public address", "", one, Result{Unavailable: true, Reason: ReasonNoPublicAddress}},
		{"no bindings", "1.2.3.4", nil, Result{Unavailable: true, Reason: ReasonNoPortBindings}},
		{"only nil bindings", "1.2.3.4", []*manmanpb.PortBinding{nil}, Result{Unavailable: true, Reason: ReasonNoPortBindings}},
		{"no address no bindings", "", nil, Result{Unavailable: true, Reason: ReasonNoPublicAddress}},
		{"host passthrough", " Play.Example.COM ", one, Result{Entries: []Entry{{" Play.Example.COM ", 25565, "TCP"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Derive(tc.host, tc.pbs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Derive(%q, %d bindings) = %+v, want %+v", tc.host, len(tc.pbs), got, tc.want)
			}
		})
	}
}

func TestEntryAddress(t *testing.T) {
	if got := (Entry{IP: "1.2.3.4", Port: 7}).Address(); got != "1.2.3.4:7" {
		t.Errorf("Address() = %q, want 1.2.3.4:7", got)
	}
}
