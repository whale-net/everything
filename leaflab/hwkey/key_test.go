package hwkey

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

// TestKey_SHT3xIsTwoSensorRows covers the "one SHT3x is two sensor rows at
// one address" requirement: two keys sharing the same I2CAddress and
// MuxPath but differing only in SensorTypeID (temperature vs humidity) must
// compare unequal, and both resolve independently via SQLPredicate.
func TestKey_SHT3xIsTwoSensorRows(t *testing.T) {
	addr := Address(0x44) // a real SHT3x address
	temperature := Key{I2CAddress: addr, MuxPath: MuxPath{}, SensorTypeID: 1}
	humidity := Key{I2CAddress: addr, MuxPath: MuxPath{}, SensorTypeID: 2}

	if temperature.Equal(humidity) {
		t.Fatal("keys differing only in SensorTypeID compared Equal, want unequal (one SHT3x is two sensor rows)")
	}
	if !temperature.Chip().Equal(humidity.Chip()) {
		t.Error("ChipKey (address+mux only, dropping SensorTypeID) should be Equal for the two virtual sensors of one chip")
	}

	predT, argsT := temperature.SQLPredicate(0)
	predH, argsH := humidity.SQLPredicate(0)
	if predT != predH {
		t.Errorf("SQLPredicate text differs between the two virtual sensors: %q vs %q, want identical text with differing args", predT, predH)
	}
	if len(argsT) != len(argsH) {
		t.Fatalf("SQLPredicate arg count differs: %d vs %d", len(argsT), len(argsH))
	}
	// sensor_type_id is the second positional arg (i2c_address, sensor_type_id, mux_path_text).
	if argsT[1] == argsH[1] {
		t.Errorf("SQLPredicate sensor_type_id arg identical for both virtual sensors: %v", argsT[1])
	}
}

// TestKey_Equal_AllThreeComponentsMatter proves Equal actually inspects all
// three components of the canonical key, not a subset.
func TestKey_Equal_AllThreeComponentsMatter(t *testing.T) {
	base := Key{I2CAddress: Address(26), MuxPath: MuxPath{{MuxAddress: 112, MuxChannel: 5}}, SensorTypeID: 7}

	diffAddr := base
	diffAddr.I2CAddress = Address(27)
	if base.Equal(diffAddr) {
		t.Error("Key.Equal ignored a differing I2CAddress")
	}

	diffMux := base
	diffMux.MuxPath = MuxPath{{MuxAddress: 112, MuxChannel: 6}}
	if base.Equal(diffMux) {
		t.Error("Key.Equal ignored a differing MuxPath")
	}

	diffType := base
	diffType.SensorTypeID = 8
	if base.Equal(diffType) {
		t.Error("Key.Equal ignored a differing SensorTypeID")
	}

	identical := Key{I2CAddress: Address(26), MuxPath: MuxPath{{MuxAddress: 112, MuxChannel: 5}}, SensorTypeID: 7}
	if !base.Equal(identical) {
		t.Error("Key.Equal returned false for two structurally identical keys")
	}
}

// TestKey_JSONRoundTrip_ComparesEqual proves a Key round-trips through the
// proto/JSON boundary (via its component types' MarshalJSON/UnmarshalJSON)
// and compares equal to the original -- both forms of address input
// (hex-parsed and decimal-parsed) and both forms of mux key (absent vs
// explicit 0) must land on the same Key after the round trip.
func TestKey_JSONRoundTrip_ComparesEqual(t *testing.T) {
	hexAddr, err := ParseAddress("0x1A")
	if err != nil {
		t.Fatalf("ParseAddress(0x1A): %v", err)
	}

	want := Key{
		I2CAddress:   hexAddr,
		MuxPath:      MuxPath{{MuxAddress: 112, MuxChannel: 0}},
		SensorTypeID: 42,
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got Key
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(%s): %v", data, err)
	}
	if !got.Equal(want) {
		t.Errorf("round-tripped Key %+v not Equal to original %+v (raw JSON: %s)", got, want, data)
	}

	// A key built from the decimal address form and an absent mux-channel
	// key must land on the exact same Key as one built from the hex form
	// with an explicit-0 mux-channel -- proving the two ambiguities FR18
	// closes compose correctly at the whole-Key level, not just per field.
	decAddr, err := ParseAddress("26")
	if err != nil {
		t.Fatalf("ParseAddress(26): %v", err)
	}
	altData := []byte(`{"I2CAddress":26,"MuxPath":[{"muxAddress":112}],"SensorTypeID":42}`)
	var alt Key
	if err := json.Unmarshal(altData, &alt); err != nil {
		t.Fatalf("Unmarshal(%s): %v", altData, err)
	}
	alt.I2CAddress = decAddr // exercise decimal parse path explicitly too
	if !alt.Equal(want) {
		t.Errorf("alternate-form Key %+v not Equal to canonical Key %+v", alt, want)
	}
}

// TestKey_String_IncludesAllThreeComponents is a light sanity check that
// String (used for logs/error details) doesn't silently drop a component.
func TestKey_String_IncludesAllThreeComponents(t *testing.T) {
	k := Key{I2CAddress: Address(26), MuxPath: MuxPath{{MuxAddress: 112, MuxChannel: 5}}, SensorTypeID: 7}
	s := k.String()
	for _, want := range []string{"26", "112", "5", "7"} {
		if !strings.Contains(s, want) {
			t.Errorf("Key.String() = %q, want it to contain %q", s, want)
		}
	}
}

// TestKey_SQLPredicate_PlaceholderNumberingAndAbsentBranch covers
// SQLPredicate's two shapes: a present address produces an
// i2c_address = $N branch, an absent address produces an
// i2c_address IS NULL branch, and both honour the caller-supplied
// argOffset for placeholder numbering.
func TestKey_SQLPredicate_PlaceholderNumberingAndAbsentBranch(t *testing.T) {
	present := Key{I2CAddress: Address(26), MuxPath: MuxPath{}, SensorTypeID: 5}
	pred, args := present.SQLPredicate(1)
	if !strings.Contains(pred, "i2c_address = $2") {
		t.Errorf("present-address predicate = %q, want it to reference $2 (argOffset+1) for i2c_address", pred)
	}
	if !strings.Contains(pred, "sensor_type_id = $3") {
		t.Errorf("present-address predicate = %q, want it to reference $3 for sensor_type_id", pred)
	}
	if !strings.Contains(pred, "mux_path::text = $4") {
		t.Errorf("present-address predicate = %q, want it to reference $4 for mux_path::text", pred)
	}
	if len(args) != 3 {
		t.Fatalf("present-address args = %v, want 3 values", args)
	}

	absent := Key{I2CAddress: Absent, MuxPath: MuxPath{}, SensorTypeID: 5}
	predAbsent, argsAbsent := absent.SQLPredicate(0)
	if !strings.Contains(predAbsent, "i2c_address IS NULL") {
		t.Errorf("absent-address predicate = %q, want an IS NULL branch", predAbsent)
	}
	if len(argsAbsent) != 2 {
		t.Fatalf("absent-address args = %v, want 2 values (sensor_type_id, mux_path text)", argsAbsent)
	}
}

// TestPackage_NoFunctionAcceptsDisplayString is a structural guard for
// FR18.3: no exported function in this package may take a sensor-type
// display string (e.g. a bare "temperature" string meant to be resolved to
// a SensorTypeID) as an argument. It's enforced by inspecting the package's
// own AST rather than by convention, so a future addition can't silently
// reintroduce the ambiguity FR18.3 forbids. The only string parameters this
// package legitimately accepts are ParseAddress's address string and
// AddressOpt.UnmarshalJSON/MarshalJSON's raw JSON bytes -- both named for
// what they actually are, never "sensorType"/"type"/"name".
func TestPackage_NoFunctionAcceptsDisplayString(t *testing.T) {
	// Resolve this package's own source directory via the Bazel runfiles
	// manifest (see BUILD.bazel's hwkey_test "data" attribute) -- a plain
	// relative "." path doesn't work under `bazel test`'s sandbox, which
	// runs with a working directory that doesn't contain the source tree.
	keyGo, err := runfiles.Rlocation("_main/leaflab/hwkey/key.go")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(key.go): %v (is key.go listed in hwkey_test's data attribute?)", err)
	}
	dir := filepath.Dir(keyGo)

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("ParseDir(%s): %v", dir, err)
	}
	if len(pkgs) == 0 {
		t.Fatalf("ParseDir(%s) found no packages -- runfiles resolution likely broken", dir)
	}

	suspectNames := []string{"type", "sensortype", "sensor_type", "displayname", "display_name", "label"}

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Type.Params == nil {
					return true
				}
				for _, field := range fn.Type.Params.List {
					ident, ok := field.Type.(*ast.Ident)
					if !ok || ident.Name != "string" {
						continue
					}
					for _, name := range field.Names {
						lower := strings.ToLower(name.Name)
						for _, s := range suspectNames {
							if strings.Contains(lower, s) {
								t.Errorf("func %s has string parameter %q, which looks like a sensor-type display string; FR18.3 forbids this package from accepting one", fn.Name.Name, name.Name)
							}
						}
					}
				}
				return true
			})
		}
	}
}
