package hwkey

import (
	"encoding/json"
	"testing"
)

// TestParseAddress_HexAndDecimalFormsAreTheSameKey covers FR18.2: 0x1A, 0x1a
// and 26 all parse to the same AddressOpt, compared via Equal (not just
// struct equality, to exercise the type's own comparison rule).
func TestParseAddress_HexAndDecimalFormsAreTheSameKey(t *testing.T) {
	forms := []string{"0x1A", "0x1a", "26", " 26 ", "0X1a"}
	var parsed []AddressOpt
	for _, s := range forms {
		a, err := ParseAddress(s)
		if err != nil {
			t.Fatalf("ParseAddress(%q) returned error: %v", s, err)
		}
		parsed = append(parsed, a)
	}
	for i := 1; i < len(parsed); i++ {
		if !parsed[0].Equal(parsed[i]) {
			t.Errorf("ParseAddress(%q)=%s not Equal to ParseAddress(%q)=%s, want the same key",
				forms[0], parsed[0], forms[i], parsed[i])
		}
	}
	if got := parsed[0].String(); got != "26" {
		t.Errorf("canonical String() = %q, want %q", got, "26")
	}
}

// TestParseAddress_RejectsEmpty documents that ParseAddress never produces
// Absent -- an empty string is an error, not a silent Absent, so callers
// can't accidentally conflate "no input" with "no address recorded".
func TestParseAddress_RejectsEmpty(t *testing.T) {
	if _, err := ParseAddress(""); err == nil {
		t.Error("ParseAddress(\"\") returned nil error, want a rejection")
	}
	if _, err := ParseAddress("   "); err == nil {
		t.Error("ParseAddress(\"   \") returned nil error, want a rejection")
	}
}

// TestParseAddress_RejectsGarbage covers malformed input beyond simple
// emptiness -- neither valid hex nor valid decimal.
func TestParseAddress_RejectsGarbage(t *testing.T) {
	for _, s := range []string{"0xZZ", "not-a-number", "0x", "-1"} {
		if _, err := ParseAddress(s); err == nil {
			t.Errorf("ParseAddress(%q) returned nil error, want a rejection", s)
		}
	}
}

// TestAddressOpt_AbsentNotEqualZero covers FR18.2's core distinction:
// Absent (no address recorded at all) and a present 0 (the legacy
// manifests' "unknown address" sentinel) are NOT the same key, even though
// a naive comparison of "no value" and "zero value" might conflate them.
func TestAddressOpt_AbsentNotEqualZero(t *testing.T) {
	zero := Address(0)
	if Absent.Equal(zero) {
		t.Fatal("Absent.Equal(Address(0)) = true, want false: absent and 0 must not be interchangeable (FR18.2)")
	}
	if zero.Equal(Absent) {
		t.Fatal("Address(0).Equal(Absent) = true, want false")
	}
	if !Absent.IsAbsent() {
		t.Error("Absent.IsAbsent() = false, want true")
	}
	if zero.IsAbsent() {
		t.Error("Address(0).IsAbsent() = true, want false: a present 0 is not absent")
	}
	if !zero.IsUnknownSentinel() {
		t.Error("Address(0).IsUnknownSentinel() = false, want true")
	}
	if Absent.IsUnknownSentinel() {
		t.Error("Absent.IsUnknownSentinel() = true, want false: absent is not the same state as the sentinel")
	}
}

// TestAddressOpt_EqualIsReflexiveAcrossPresentValues sanity-checks Equal
// beyond the absent/zero edge case: two distinct present, non-zero values
// must not compare equal, and identical present values must.
func TestAddressOpt_EqualIsReflexiveAcrossPresentValues(t *testing.T) {
	a := Address(26)
	b := Address(26)
	c := Address(27)
	if !a.Equal(b) {
		t.Error("Address(26).Equal(Address(26)) = false, want true")
	}
	if a.Equal(c) {
		t.Error("Address(26).Equal(Address(27)) = true, want false")
	}
}

// TestAddressOpt_JSONRoundTrip covers the proto/JSON boundary encoding:
// Absent marshals to null and back to Absent; a present value (including
// 0) marshals to a JSON number and back to the same present value.
// Marshal->Unmarshal->Marshal must be idempotent (stable canonicalisation).
func TestAddressOpt_JSONRoundTrip(t *testing.T) {
	cases := []AddressOpt{Absent, Address(0), Address(26), Address(0x1A)}
	for _, want := range cases {
		data, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("Marshal(%s): %v", want, err)
		}

		var got AddressOpt
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal(%s) of %s: %v", data, want, err)
		}
		if !got.Equal(want) {
			t.Errorf("round trip of %s: got %s, want equal to original", want, got)
		}

		// Marshal again -- must reproduce the exact same bytes (idempotent
		// canonicalisation), not merely an Equal value.
		data2, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("re-Marshal(%s): %v", got, err)
		}
		if string(data2) != string(data) {
			t.Errorf("Marshal->Unmarshal->Marshal not stable for %s: got %s, want %s", want, data2, data)
		}
	}

	// null decodes to Absent explicitly.
	var fromNull AddressOpt
	if err := json.Unmarshal([]byte("null"), &fromNull); err != nil {
		t.Fatalf("Unmarshal(null): %v", err)
	}
	if !fromNull.IsAbsent() {
		t.Errorf("Unmarshal(null) = %s, want Absent", fromNull)
	}

	// A present 0 decodes distinctly from null.
	var fromZero AddressOpt
	if err := json.Unmarshal([]byte("0"), &fromZero); err != nil {
		t.Fatalf("Unmarshal(0): %v", err)
	}
	if fromZero.IsAbsent() {
		t.Error("Unmarshal(0) reported IsAbsent() = true, want a present 0 (the unknown sentinel)")
	}
	if fromNull.Equal(fromZero) {
		t.Error("Unmarshal(null) Equal Unmarshal(0), want them distinct (FR18.2)")
	}
}

// TestAddressOpt_PtrRoundTrip covers AddressFromPtr/Ptr, the *uint32 idiom
// used for an `optional uint32` proto3 field: nil <-> Absent, a pointer <->
// a present value, round-tripping through both directions.
func TestAddressOpt_PtrRoundTrip(t *testing.T) {
	if got := AddressFromPtr(nil); !got.IsAbsent() {
		t.Errorf("AddressFromPtr(nil) = %s, want Absent", got)
	}
	if got := Absent.Ptr(); got != nil {
		t.Errorf("Absent.Ptr() = %v, want nil", got)
	}

	v := uint32(26)
	present := AddressFromPtr(&v)
	if present.IsAbsent() {
		t.Fatal("AddressFromPtr(&26) reported IsAbsent() = true")
	}
	got := present.Ptr()
	if got == nil || *got != v {
		t.Errorf("present.Ptr() = %v, want pointer to %d", got, v)
	}

	zero := uint32(0)
	sentinel := AddressFromPtr(&zero)
	if sentinel.IsAbsent() {
		t.Error("AddressFromPtr(&0) reported IsAbsent() = true, want a present 0 (sentinel)")
	}
	if !sentinel.IsUnknownSentinel() {
		t.Error("AddressFromPtr(&0) is not the unknown sentinel")
	}
}
