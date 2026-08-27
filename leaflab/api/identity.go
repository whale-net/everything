package main

import (
	"context"

	configpb "github.com/whale-net/everything/firmware/proto/config"
)

// checkPushConfigIdentity is FR17's pre-write identity check: it runs
// before PushDeviceConfig writes or publishes anything (the "real push
// path" -- FR17 explicitly is not satisfied by a dry-run-only check, per
// FR82), and decides whether every entry in sensors continues an existing
// sensor's identity (FR16 cases 1/2, via Repository.FindSensorIDByHWKey /
// FindSensorIDByName) or whether any entry would establish a genuinely new
// one (case 3).
//
// Case 3 entries must be refused via contract.Refuse -- naming the
// consequence ("history will not follow") and the RewireSensor RPC as the
// alternative -- before InsertDeviceConfigNextVersion or the MQTT publish
// runs, per FR17. This is also where FR16.4's swap detection belongs: two
// entries that would exchange two existing sensors' hardware keys must
// either be applied as an atomic swap preserving both identities, or be
// refused naming both entries -- a case FR39's within-payload collision
// check does not cover, because a swap is not a within-payload collision
// (each entry's own address/name is unique within the payload; the
// collision is against the pre-push DB state).
//
// TODO(Implementation phase): resolve every entry via
// Repository.FindSensorIDByHWKey / FindSensorIDByName, detect FR16.4's
// swap case among entries that resolve via case 1, and return
// contract.Refuse for any case-3 entry or unresolved swap. Returning nil
// unconditionally, as this scaffold does, performs no refusal yet.
func (s *LeafLabAPIServer) checkPushConfigIdentity(ctx context.Context, boardID int64, sensors []*configpb.SensorConfig) error {
	_ = ctx
	_ = boardID
	_ = sensors
	return nil
}
