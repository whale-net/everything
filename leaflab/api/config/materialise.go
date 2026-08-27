package config

import "errors"

// ErrNoAcceptedConfig is returned by Materialise when base is nil,
// signalling an EDIT-scope push against a board with no accepted config
// -- FR82.3's exact refusal condition ("this board has no accepted config
// to complete your edit from; send a complete push"). The caller
// (PushDeviceConfig's handler) turns this into contract.Refuse with that
// exact stated sentence; this package stays free of leaflab/api/contract
// so it has no gRPC-facing error of its own to build.
var ErrNoAcceptedConfig = errors.New("config: no accepted config to materialise an edit against")

// RemovedEntry is one base entry Materialise dropped, plus the RemoveForm
// of whichever RemoveKey matched it -- FR82.4's "which form was used"
// caller-visible statement, and the input the write path needs to close
// that entry's sensor_hw_history interval at the new version's
// accepted-at time (Implementation-phase work; not done here).
type RemovedEntry struct {
	Entry Entry
	Form  RemoveForm
}

// Result is Materialise's output: the new config version's complete entry
// set (every entry provenance-tagged, FR82.4) plus the entries it dropped
// and how each was named.
type Result struct {
	Entries []Entry
	Removed []RemovedEntry
}

// Materialise applies an EDIT-scope push's adds and removes against base
// -- the board's current accepted config version's entries alone (FR82.3:
// never the reported manifest, FR49). adds are the caller's named
// add/change list, already canonicalised (CanonicalKey) and carrying
// Sensor; their Provenance field is ignored on input and always set to
// ProvenanceAuthored on output, since naming an entry in adds is itself
// what makes it authored. Every base entry not named in adds or dropped
// by removes carries forward unchanged with ProvenanceMaterialised.
//
// base == nil is Materialise's signal for "no accepted config exists for
// this board" and returns ErrNoAcceptedConfig without consulting adds or
// removes at all -- a board with a genuinely empty (zero-sensor) accepted
// config must be represented as a non-nil, empty slice by the caller, to
// keep that case (a legitimate EDIT base) distinguishable from "there was
// never an accepted push at all".
//
// A key present in both adds and a RemoveKey's match set is not a
// contradiction this function detects or refuses: the add wins (it is
// applied after removal accounting), since naming an entry as an
// add/change is a stronger, more specific caller statement than a
// possibly-chip-key removal that happens to also match it. Whether that
// combination should be refused up front as caller error is FR39
// validation's call, not materialisation's.
func Materialise(base []Entry, adds []Entry, removes []RemoveKey) (Result, error) {
	if base == nil {
		return Result{}, ErrNoAcceptedConfig
	}

	addedKeys := make(map[string]bool, len(adds))
	for _, a := range adds {
		addedKeys[a.Key.String()] = true
	}

	var removed []RemovedEntry
	removedKeys := make(map[string]bool)
	for _, rk := range removes {
		matched, err := rk.Match(base)
		if err != nil {
			return Result{}, err
		}
		for _, e := range matched {
			k := e.Key.String()
			if addedKeys[k] {
				// Superseded by an authored add in this same push -- not
				// actually removed (adds win over removes; see doc
				// comment). Its hardware-history interval must not be
				// closed: the entry is still present in Result.Entries.
				continue
			}
			if removedKeys[k] {
				// Already dropped by an earlier remove key in this same
				// push (e.g. a chip-key remove and a full-key remove both
				// naming one of that chip's entries) -- record it once,
				// against whichever remove key matched it first.
				continue
			}
			removedKeys[k] = true
			removed = append(removed, RemovedEntry{Entry: e, Form: rk.Form()})
		}
	}

	entries := make([]Entry, 0, len(base)+len(adds))
	for _, a := range adds {
		a.Provenance = ProvenanceAuthored
		entries = append(entries, a)
	}

	for _, e := range base {
		k := e.Key.String()
		if removedKeys[k] || addedKeys[k] {
			// Dropped by a remove key, or superseded by an authored add
			// above (adds win over removes -- see doc comment).
			continue
		}
		e.Provenance = ProvenanceMaterialised
		entries = append(entries, e)
	}

	return Result{Entries: entries, Removed: removed}, nil
}
