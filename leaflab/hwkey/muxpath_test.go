package hwkey

import (
	"encoding/json"
	"testing"
)

// TestMuxHop_AbsentKeyAndExplicitZero_DecodeIdentically covers FR18.1's
// first requirement: an absent muxAddress/muxChannel key and an explicit 0
// must decode to the same MuxHop value, both before and after
// canonicalisation.
func TestMuxHop_AbsentKeyAndExplicitZero_DecodeIdentically(t *testing.T) {
	forms := []string{
		`{"muxAddress": 112, "muxChannel": 0}`,
		`{"muxAddress": 112}`,
	}
	var hops []MuxHop
	for _, s := range forms {
		var h MuxHop
		if err := json.Unmarshal([]byte(s), &h); err != nil {
			t.Fatalf("Unmarshal(%s): %v", s, err)
		}
		hops = append(hops, h)
	}
	if hops[0] != hops[1] {
		t.Errorf("absent muxChannel and explicit 0 decoded to different values: %+v vs %+v", hops[0], hops[1])
	}
	if hops[0].MuxChannel != 0 {
		t.Errorf("MuxChannel = %d, want 0", hops[0].MuxChannel)
	}

	// Same check for muxAddress.
	var absentAddr, explicitZeroAddr MuxHop
	if err := json.Unmarshal([]byte(`{"muxChannel": 5}`), &absentAddr); err != nil {
		t.Fatalf("Unmarshal absent muxAddress: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"muxAddress": 0, "muxChannel": 5}`), &explicitZeroAddr); err != nil {
		t.Fatalf("Unmarshal explicit 0 muxAddress: %v", err)
	}
	if absentAddr != explicitZeroAddr {
		t.Errorf("absent muxAddress and explicit 0 decoded to different values: %+v vs %+v", absentAddr, explicitZeroAddr)
	}
}

// TestMuxHop_FractionalAndIntegerForms_DecodeIdentically covers FR18.1's
// integral-fractional ambiguity: "112" and "112.0" (both integral values,
// just written differently) must decode and re-encode to the same
// canonical form -- 112 never 112.0.
func TestMuxHop_FractionalAndIntegerForms_DecodeIdentically(t *testing.T) {
	var fromInt, fromFraction MuxHop
	if err := json.Unmarshal([]byte(`{"muxAddress": 112, "muxChannel": 5}`), &fromInt); err != nil {
		t.Fatalf("Unmarshal integer form: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"muxAddress": 112.0, "muxChannel": 5.0}`), &fromFraction); err != nil {
		t.Fatalf("Unmarshal fractional form: %v", err)
	}
	if fromInt != fromFraction {
		t.Fatalf("integer and fractional forms decoded to different values: %+v vs %+v", fromInt, fromFraction)
	}

	path := MuxPath{fromFraction}
	// json.Marshal re-compacts whatever MarshalJSON returns (insignificant
	// whitespace is not preserved through the standard encoder), so assert
	// against the compact form here; SQLText's own spaced form is checked
	// directly (not through encoding/json) in
	// TestMuxPath_SQLText_MatchesPostgresJSONBTextRendering below.
	data, err := json.Marshal(path)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := string(data); got != `[{"muxAddress":112,"muxChannel":5}]` {
		t.Errorf("Marshal of value decoded from fractional form = %s, want integer form (no .0)", got)
	}
}

// TestMuxHop_RejectsNonIntegralFraction covers the boundary of the
// integral-fractional normalisation: a genuinely fractional value (not
// integral, e.g. 112.5) is not a valid mux address/channel and must be
// rejected, not silently truncated.
func TestMuxHop_RejectsNonIntegralFraction(t *testing.T) {
	var h MuxHop
	if err := json.Unmarshal([]byte(`{"muxAddress": 112.5, "muxChannel": 5}`), &h); err == nil {
		t.Error("Unmarshal with muxAddress=112.5 returned nil error, want a rejection")
	}
}

// TestMuxPath_MarshalUnmarshalMarshal_IsStable proves idempotent
// canonicalisation across a whole chain, including the empty (root-bus)
// case and a multi-hop cascade.
func TestMuxPath_MarshalUnmarshalMarshal_IsStable(t *testing.T) {
	cases := []MuxPath{
		{},
		{{MuxAddress: 112, MuxChannel: 5}},
		{{MuxAddress: 112, MuxChannel: 3}, {MuxAddress: 113, MuxChannel: 1}},
	}
	for _, want := range cases {
		data, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", want, err)
		}
		var got MuxPath
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		if !got.Equal(want) {
			t.Errorf("round trip of %v: got %v, want equal to original", want, got)
		}
		data2, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("re-Marshal(%v): %v", got, err)
		}
		if string(data2) != string(data) {
			t.Errorf("Marshal->Unmarshal->Marshal not stable for %v: got %s, want %s", want, data2, data)
		}
	}
}

// TestMuxPath_NullDecodesToEmpty covers JSON null as an alternate spelling
// of "directly on the root bus", same as `[]`.
func TestMuxPath_NullDecodesToEmpty(t *testing.T) {
	var p MuxPath
	if err := json.Unmarshal([]byte("null"), &p); err != nil {
		t.Fatalf("Unmarshal(null): %v", err)
	}
	if !p.Equal(MuxPath{}) {
		t.Errorf("Unmarshal(null) = %v, want empty MuxPath", p)
	}
	if got := string(p.SQLText()); got != "[]" {
		t.Errorf("SQLText() of null-decoded MuxPath = %q, want %q", got, "[]")
	}
}

// TestMuxPath_SQLText_MatchesPostgresJSONBTextRendering documents the
// exact byte format SQLText produces, matching Postgres's own
// `mux_path::text` rendering of a jsonb value written in this canonical
// form: a space after ':' and after ',', decimal integers, no trailing
// fractional part. See TestMuxPath_SQLText_MatchesPostgresRendering in
// sqlpredicate_integration_test.go for the corroborating check against a
// real database.
func TestMuxPath_SQLText_MatchesPostgresJSONBTextRendering(t *testing.T) {
	cases := []struct {
		path MuxPath
		want string
	}{
		{MuxPath{}, `[]`},
		{MuxPath{{MuxAddress: 112, MuxChannel: 5}}, `[{"muxAddress": 112, "muxChannel": 5}]`},
		{
			MuxPath{{MuxAddress: 112, MuxChannel: 3}, {MuxAddress: 113, MuxChannel: 1}},
			`[{"muxAddress": 112, "muxChannel": 3}, {"muxAddress": 113, "muxChannel": 1}]`,
		},
	}
	for _, tc := range cases {
		if got := tc.path.SQLText(); got != tc.want {
			t.Errorf("SQLText(%v) = %q, want %q", tc.path, got, tc.want)
		}
		if got := tc.path.String(); got != tc.want {
			t.Errorf("String(%v) = %q, want %q (String must match SQLText)", tc.path, got, tc.want)
		}
	}
}

// TestMuxPath_Equal_OrderMatters covers that MuxPath is an ordered chain,
// not a set -- reversing hop order must not compare equal.
func TestMuxPath_Equal_OrderMatters(t *testing.T) {
	a := MuxPath{{MuxAddress: 112, MuxChannel: 3}, {MuxAddress: 113, MuxChannel: 1}}
	b := MuxPath{{MuxAddress: 113, MuxChannel: 1}, {MuxAddress: 112, MuxChannel: 3}}
	if a.Equal(b) {
		t.Error("MuxPath.Equal treated reversed hop order as equal, want order to matter")
	}
}
