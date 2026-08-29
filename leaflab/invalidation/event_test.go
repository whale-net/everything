package invalidation_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/whale-net/everything/leaflab/invalidation"
)

// TestEvent_JSONRoundTrip is a scaffold-level sanity check that Event
// survives the wire encoding Publisher/Subscriber use (see publisher.go's
// and subscriber.go's use of encoding/json). The behavioural guarantees
// FR73 actually requires -- every-replica fanout, dropped-event recovery,
// rename evicting the prior key -- need a real broker and are covered by
// leaflab/processor's integration tests, not this package (see
// BUILD.bazel's doc comment on invalidation_test).
func TestEvent_JSONRoundTrip(t *testing.T) {
	want := invalidation.Event{
		Kind:            invalidation.KindName,
		DeviceID:        "device-1",
		SensorID:        42,
		SensorName:      "new-name",
		PriorSensorName: "old-name",
		ObservedAt:      time.Now().UTC().Truncate(time.Millisecond),
	}

	body, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got invalidation.Event
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got != want {
		t.Errorf("round trip mismatch:\n want %+v\n got  %+v", want, got)
	}
}
