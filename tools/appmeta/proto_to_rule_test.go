package appmeta

import (
	"os"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"

	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// fixtureMetadataPath is the metadata JSON produced by the full-coverage
// fixture in //tools/appmeta/testdata, declared as `data` on this test so
// Bazel builds it as a normal (cheap) dependency — no nested bazel needed.
const fixtureMetadataPath = "testdata/fixture-app_metadata_metadata.json"

// TestFixtureLeavesNoFieldUnset is the proto -> rule direction of the
// contract: //tools/appmeta/testdata sets every release_app attribute, so
// decoding its emitted manifest must leave no AppManifest field unset. If a
// field is added to appmeta.proto that the rule never populates (or the
// fixture never exercises), this test catches it. See README.md.
func TestFixtureLeavesNoFieldUnset(t *testing.T) {
	data, err := os.ReadFile(fixtureMetadataPath)
	if err != nil {
		t.Fatalf("read fixture metadata: %v", err)
	}

	var manifest appmetapb.AppManifest
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode fixture metadata: %v", err)
	}

	assertNoFieldUnset(t, manifest.ProtoReflect())
}

// assertNoFieldUnset walks every field declared on m's message type and
// fails if any is left at its zero value / unpopulated. Recurses into
// message-typed fields (HealthCheck, Ingress, Resources) so nested fields
// are covered too.
func assertNoFieldUnset(t *testing.T, m protoreflect.Message) {
	t.Helper()
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !m.Has(fd) {
			t.Errorf("field %q on %s was left unset by the fixture", fd.Name(), m.Descriptor().FullName())
			continue
		}
		if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && !fd.IsMap() {
			val := m.Get(fd)
			assertNoFieldUnset(t, val.Message())
		}
	}
}
