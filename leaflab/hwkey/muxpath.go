package hwkey

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// MuxHop is one step in a cascaded I2C mux chain: the mux's own I2C
// address and the channel on that mux the next hop (or the sensor) is
// wired to. See leaflab/DATA.md "mux_path JSONB Format".
type MuxHop struct {
	MuxAddress uint32
	MuxChannel uint32
}

// MuxPath is the ordered outer->inner chain of MuxHop leading to a
// sensor; an empty MuxPath means the sensor is directly on the root I2C
// bus.
//
// MuxPath has exactly one canonical encoding (FR18.1): an absent
// muxAddress or muxChannel key and an explicit 0 both canonicalise to the
// same {"muxAddress": N, "muxChannel": N} object with every key present,
// integer fields are never emitted with a fractional part, and object
// keys are emitted in a fixed, alphabetical order. This exact format --
// including the single space after ':' and ',' -- matches Postgres's own
// `jsonb::text` rendering (see SQLText), so a MuxPath canonicalised here
// and one round-tripped through the sensor.mux_path column compare equal
// as plain strings. Postgres's jsonb type does not do this normalisation
// itself: it preserves a numeral's original fractional formatting and
// does not backfill an omitted key to its zero value, so the ambiguity
// FR18.1 closes has to be closed here, at the API boundary, before
// anything reaches the database.
type MuxPath []MuxHop

// SQLText renders the exact text idx_sensor_hw_address's
// `(mux_path::text)` expression produces for the semantically equal jsonb
// value, once mux_path was written using this package's canonical
// encoding. See Key.SQLPredicate.
func (p MuxPath) SQLText() string {
	if len(p) == 0 {
		return "[]"
	}
	parts := make([]string, len(p))
	for i, h := range p {
		parts[i] = fmt.Sprintf(`{"muxAddress": %d, "muxChannel": %d}`, h.MuxAddress, h.MuxChannel)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// String is MuxPath's stable, canonical text form, used for logs and
// error details as well as SQL text matching (SQLText).
func (p MuxPath) String() string { return p.SQLText() }

// Equal compares two MuxPath values semantically. Order matters --
// mux_path is an ordered outer->inner chain, not a set.
func (p MuxPath) Equal(other MuxPath) bool {
	if len(p) != len(other) {
		return false
	}
	for i := range p {
		if p[i] != other[i] {
			return false
		}
	}
	return true
}

// MarshalJSON is the one canonical proto/JSON-boundary encoding for
// MuxPath (FR18.1): see the MuxPath doc comment. The result is compact,
// valid JSON -- the extra spacing after ':' and ',' is insignificant
// whitespace to any JSON parser, but is what lets SQLText double as this
// type's JSON encoding.
func (p MuxPath) MarshalJSON() ([]byte, error) {
	return []byte(p.SQLText()), nil
}

// muxHopWire decodes a mux hop's raw JSON with numeric fields kept as
// json.Number so an absent key (empty json.Number) and a value written
// with a fractional part (e.g. 112.0) can both be normalised explicitly.
// encoding/json's default behaviour for integer Go fields rejects
// "112.0" outright, even though it is integral -- exactly the ambiguity
// FR18.1 requires this package to close instead of reject.
type muxHopWire struct {
	MuxAddress json.Number `json:"muxAddress"`
	MuxChannel json.Number `json:"muxChannel"`
}

func parseIntegralUint32(n json.Number, field string) (uint32, error) {
	if n == "" {
		return 0, nil
	}
	f, err := n.Float64()
	if err != nil {
		return 0, fmt.Errorf("hwkey: %s: %w", field, err)
	}
	if f < 0 || f > math.MaxUint32 || f != math.Trunc(f) {
		return 0, fmt.Errorf("hwkey: %s: %v is not a valid mux hop field", field, n)
	}
	return uint32(f), nil
}

// UnmarshalJSON is MarshalJSON's inverse for one hop. Per FR18.1, an
// absent muxAddress or muxChannel key and an explicit 0 decode to the
// identical MuxHop value -- both forms round-trip to the same canonical
// encoding on the next MarshalJSON.
func (h *MuxHop) UnmarshalJSON(data []byte) error {
	var wire muxHopWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("hwkey: invalid mux hop: %w", err)
	}
	addr, err := parseIntegralUint32(wire.MuxAddress, "muxAddress")
	if err != nil {
		return err
	}
	ch, err := parseIntegralUint32(wire.MuxChannel, "muxChannel")
	if err != nil {
		return err
	}
	h.MuxAddress = addr
	h.MuxChannel = ch
	return nil
}

// UnmarshalJSON is MarshalJSON's inverse for the whole chain. JSON null
// decodes to an empty MuxPath (directly on the root bus), same as `[]`.
func (p *MuxPath) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		*p = MuxPath{}
		return nil
	}
	var hops []MuxHop
	if err := json.Unmarshal(data, &hops); err != nil {
		return fmt.Errorf("hwkey: invalid mux_path: %w", err)
	}
	if hops == nil {
		hops = MuxPath{}
	}
	*p = hops
	return nil
}
