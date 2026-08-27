// Package config implements FR82's config push scope semantics: canonical
// entry keying, EDIT-scope materialisation against a base config version,
// and FR82.4 removal-key resolution (full canonical key vs chip key).
//
// This package has no database dependency and never calls
// leaflab/api/contract: it operates entirely on in-memory hwkey.Key /
// *configpb.SensorConfig values that its caller (leaflab/api/server.go's
// PushDeviceConfig handler, and its supporting Repository methods) has
// already loaded and resolved (in particular, every SensorTypeID here is
// already resolved -- this package never talks to the sensor_type
// catalog). That split keeps this package pure and independently
// unit-testable, and keeps the DB/gRPC-specific error translation (a
// contract.Refuse with FR82's exact stated sentence, a contract.New with a
// distinct FailureClass) at the one call site that has a
// contract.FailureClass to pick.
//
// Scope:
//   - COMPLETE (FR82.2): every entry in the push is "authored" -- this
//     package's role is limited to CanonicalKey (below), used to detect
//     within-payload duplicates and to populate device_config_entry
//     (migration 028). There is no base to materialise against.
//   - EDIT (FR82.3/FR82.4): Materialise combines a base (the board's
//     current accepted config version's entries, loaded by the caller --
//     never the reported manifest, FR49) with an authored add/change list
//     and a RemoveKey list, producing the new version's complete entry set
//     plus per-entry provenance and FR82.4's caller-visible "which removal
//     form was used" accounting.
package config
