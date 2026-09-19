package store_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// forbiddenDeliveryShipmentMethodSubstrings names the verb shapes NFR2/LB3
// forbid anywhere on DeliveryShipmentStore: a shipment is an addition to
// history, never an overwrite or removal, so no method on this
// interface -- now or later -- may look like a mutation-in-place or a
// deletion of an existing row. Mirrors
// milestone_status_nfr2_test.go's forbiddenMilestoneStatusMethodSubstrings
// (that file's own package cannot be imported from here, so this list is
// duplicated rather than shared).
var forbiddenDeliveryShipmentMethodSubstrings = []string{"Update", "Delete", "Remove", "Edit", "Modify", "Patch", "Overwrite"}

// TestDeliveryShipmentStore_InterfaceExposesOnlyAppendOnlyMethods_NFR2 is
// issue #2686's Testing item 3's structural half: reflects over
// store.DeliveryShipmentStore itself (not a fake or a mock) and asserts
// its method set is exactly the three accessors (MarkShipped,
// ShippedEntityIDs, DeliveryBreakdown) -- no more, no fewer -- and that
// none of them carries an update/delete-shaped name. A future change that
// adds a fourth method to this interface must update
// expectedDeliveryShipmentStoreMethods deliberately; it cannot silently
// slip in an update-or-delete-shaped method unnoticed.
func TestDeliveryShipmentStore_InterfaceExposesOnlyAppendOnlyMethods_NFR2(t *testing.T) {
	expected := []string{"MarkShipped", "ShippedEntityIDs", "DeliveryBreakdown"}

	typ := reflect.TypeOf((*store.DeliveryShipmentStore)(nil)).Elem()
	require.Equal(t, len(expected), typ.NumMethod(), "DeliveryShipmentStore must expose exactly these methods -- nothing more, nothing fewer: %v", expected)

	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		got = append(got, name)

		for _, forbidden := range forbiddenDeliveryShipmentMethodSubstrings {
			assert.NotContains(t, name, forbidden,
				"DeliveryShipmentStore.%s looks like an update/delete method -- NFR2/LB3 forbid any such path: a shipment is an addition to history, never an overwrite", name)
		}
	}
	assert.ElementsMatch(t, expected, got)
}

// forbiddenDeliveryShipmentSQLVerbs are the SQL statement shapes NFR2/LB3
// forbid against delivery_shipment: an UPDATE or a DELETE targeting this
// table would mutate or erase the shipment history in place.
var forbiddenDeliveryShipmentSQLVerbs = []string{"UPDATE delivery_shipment", "DELETE FROM delivery_shipment", "DELETE delivery_shipment", "TRUNCATE delivery_shipment"}

// TestDeliveryShipmentStore_SourceNeverIssuesUpdateOrDeleteSQL_NFR2 is
// issue #2686's Testing item 3's other structural half, mirroring
// milestone_status_nfr2_test.go's own technique (read the package's own
// source file back, declared as `data` on this test's BUILD.bazel
// target, rather than needing a database): delivery_shipment.go -- the
// only file in this repository that writes to `delivery_shipment` --
// must never contain an UPDATE, DELETE, or TRUNCATE statement naming
// that table.
func TestDeliveryShipmentStore_SourceNeverIssuesUpdateOrDeleteSQL_NFR2(t *testing.T) {
	src, err := os.ReadFile("delivery_shipment.go")
	if err != nil {
		t.Fatalf("read delivery_shipment.go back (is it declared as `data` on this go_test target?): %v", err)
	}
	body := strings.ToUpper(string(src))
	for _, forbidden := range forbiddenDeliveryShipmentSQLVerbs {
		assert.NotContains(t, body, strings.ToUpper(forbidden),
			"delivery_shipment.go must never issue %q -- NFR2/LB3: delivery_shipment is append-only, a row once written is never mutated or removed", forbidden)
	}
}
