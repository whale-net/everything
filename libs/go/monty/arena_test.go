package monty

import (
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/whale-net/everything/libs/go/monty/protos"
)

func intN(v int64) *pb.MontyNode { return &pb.MontyNode{Kind: &pb.MontyNode_Int{Int: v}} }

func listN(items ...uint32) *pb.MontyNode {
	return &pb.MontyNode{Kind: &pb.MontyNode_List{List: &pb.Indexes{Items: items}}}
}

// TestRoundTrip covers every supported type in both directions: a Go value
// encodes to an arena and decodes back to the value the mapping promises.
func TestRoundTrip(t *testing.T) {
	wide := new(big.Int).Lsh(big.NewInt(1), 100) // 2^100, does not fit int64
	negWide := new(big.Int).Neg(wide)

	for _, tc := range []struct {
		name string
		in   any
		want any
	}{
		{"none", nil, nil},
		{"true", true, true},
		{"false", false, false},
		{"int", int64(42), int64(42)},
		{"negative int", int64(-42), int64(-42)},
		{"min int64", int64(math.MinInt64), int64(math.MinInt64)},
		{"max int64", int64(math.MaxInt64), int64(math.MaxInt64)},
		{"go int narrows", 7, int64(7)},
		{"float", 3.5, 3.5},
		{"float32 widens", float32(1.5), 1.5},
		{"str", "hello", "hello"},
		{"empty str", "", ""},
		{"bytes", []byte{0x00, 0xff, 0x10}, []byte{0x00, 0xff, 0x10}},
		{"empty bytes", []byte{}, []byte{}},
		{"empty list", []any{}, []any{}},
		{"list", []any{int64(1), "two", nil}, []any{int64(1), "two", nil}},
		{"tuple", Tuple{int64(1), int64(2)}, Tuple{int64(1), int64(2)}},
		{"set", Set{"a", "b"}, Set{"a", "b"}},
		{"frozen set", FrozenSet{int64(1)}, FrozenSet{int64(1)}},
		{"dict", NewDict([]any{"k"}, []any{int64(1)}), NewDict([]any{"k"}, []any{int64(1)})},
		{"empty dict", NewDict(nil, nil), &Dict{Keys: []any{}, Values: []any{}}},
		// A big.Int that fits int64 goes down the sint64 arm and comes back
		// narrowed, exactly as a decoded bigint does.
		{"small bigint narrows", big.NewInt(5), int64(5)},
		{"wide bigint stays big", wide, wide},
		{"negative wide bigint", negWide, negWide},
		{
			"nested",
			[]any{
				[]any{int64(1), []any{int64(2)}},
				Tuple{NewDict([]any{true}, []any{"v"})},
			},
			[]any{
				[]any{int64(1), []any{int64(2)}},
				Tuple{NewDict([]any{true}, []any{"v"})},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arena, roots, err := EncodeArena([]any{tc.in})
			require.NoError(t, err)
			require.Len(t, roots, 1)
			assert.Equal(t, uint32(len(arena.GetNodes())), arena.GetNodeCount())

			got, err := DecodeArena(arena, roots)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0])
		})
	}
}

// TestDecodeGoldenArena decodes a hand-built arena, so decode is exercised
// independently of whatever encode happens to emit.
func TestDecodeGoldenArena(t *testing.T) {
	// post-order: 0=str, 1=int, 2=list sharing node 1 twice, 3=root
	arena := &pb.Arena{
		NodeCount: 4,
		Nodes: []*pb.MontyNode{
			{Kind: &pb.MontyNode_Str{Str: "a"}},
			intN(1),
			listN(1, 1, 0),
			{Kind: &pb.MontyNode_Tuple{Tuple: &pb.Indexes{Items: []uint32{2}}}},
		},
	}
	got, err := DecodeArena(arena, []uint32{3})
	require.NoError(t, err)
	assert.Equal(t, []any{Tuple{[]any{int64(1), int64(1), "a"}}}, got)
}

func TestDecodeMultipleRootsInOrder(t *testing.T) {
	arena := &pb.Arena{
		NodeCount: 2,
		Nodes:     []*pb.MontyNode{intN(1), intN(2)},
	}
	got, err := DecodeArena(arena, []uint32{1, 0})
	require.NoError(t, err)
	assert.Equal(t, []any{int64(2), int64(1)}, got)

	got, err = DecodeArena(arena, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDecodeNilArena(t *testing.T) {
	_, err := DecodeArena(nil, nil)
	assert.Error(t, err)
}

// TestDecodeBigIntWidth covers the big.Int/int64 boundary in both directions.
func TestDecodeBigIntWidth(t *testing.T) {
	wide := new(big.Int).Lsh(big.NewInt(1), 100)

	arena := &pb.Arena{Nodes: []*pb.MontyNode{
		{Kind: &pb.MontyNode_Bigint{Bigint: &pb.BigInt{Magnitude: wide.Bytes()}}},
		{Kind: &pb.MontyNode_Bigint{Bigint: &pb.BigInt{Magnitude: []byte{0x7b}}}},
		{Kind: &pb.MontyNode_Bigint{Bigint: &pb.BigInt{Negative: true, Magnitude: []byte{0x7b}}}},
		{Kind: &pb.MontyNode_Bigint{Bigint: &pb.BigInt{}}},
	}}
	got, err := DecodeArena(arena, []uint32{0, 1, 2, 3})
	require.NoError(t, err)
	require.Len(t, got, 4)

	assert.Equal(t, wide, got[0], "wider than 64 bits stays a *big.Int")
	assert.Equal(t, int64(123), got[1], "fits in 64 bits, narrows to int64")
	assert.Equal(t, int64(-123), got[2])
	assert.Equal(t, int64(0), got[3], "empty magnitude is zero")
}

// TestEncodeSharedSubobject is the point of the arena: one object passed under
// two names is one node.
func TestEncodeSharedSubobject(t *testing.T) {
	shared := []any{int64(1), "two"}
	arena, roots, err := EncodeArena([]any{shared, shared})
	require.NoError(t, err)

	// Two leaves plus the list itself.
	require.Len(t, arena.GetNodes(), 3)
	assert.Equal(t, []uint32{2, 2}, roots, "both roots name the same node")

	got, err := DecodeArena(arena, roots)
	require.NoError(t, err)
	assert.Equal(t, []any{shared, shared}, got)
	// The decoded references share one backing array, not two equal copies.
	assert.Same(t, &got[0].([]any)[0], &got[1].([]any)[0])
}

func TestEncodeSharedContainers(t *testing.T) {
	tup := Tuple{int64(9)}
	dict := NewDict([]any{"k"}, []any{int64(1)})
	wide := new(big.Int).Lsh(big.NewInt(1), 100)

	arena, roots, err := EncodeArena([]any{tup, dict, wide, wide, Set{}, FrozenSet{}})
	require.NoError(t, err)
	// int(9) + tuple, "k" + int(1) + dict, one bigint, two empty containers.
	require.Len(t, arena.GetNodes(), 8)
	assert.Equal(t, []uint32{1, 4, 5, 5, 6, 7}, roots)
	assert.Equal(t, roots[2], roots[3], "the same *big.Int is one node")
}

func TestEncodePostOrder(t *testing.T) {
	// [1, [2]] must be 1, 2, inner list, outer list.
	arena, roots, err := EncodeArena([]any{[]any{int64(1), []any{int64(2)}}})
	require.NoError(t, err)
	require.Equal(t, []uint32{3}, roots)

	inner := arena.GetNodes()[2].GetList()
	require.NotNil(t, inner)
	assert.Equal(t, []uint32{1}, inner.GetItems(), "inner list holds the index of node 1")
	outer := arena.GetNodes()[3].GetList()
	require.NotNil(t, outer)
	assert.Equal(t, []uint32{0, 2}, outer.GetItems())
}

func TestEncodeNodeCount(t *testing.T) {
	arena, _, err := EncodeArena([]any{int64(1), "a", []any{int64(2)}})
	require.NoError(t, err)
	assert.Equal(t, uint32(4), arena.GetNodeCount())
	assert.Len(t, arena.GetNodes(), 4)

	empty, roots, err := EncodeArena(nil)
	require.NoError(t, err)
	assert.Empty(t, roots)
	assert.Equal(t, uint32(0), empty.GetNodeCount())
}

func TestEncodeRejectsGoMap(t *testing.T) {
	_, _, err := EncodeArena([]any{map[string]any{"a": 1}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "*monty.Dict")
}

func TestEncodeRejectsUnsupportedTypes(t *testing.T) {
	for _, v := range []any{
		uint64(1), // no lossless int64 spelling
		struct{ A int }{},
		make(chan int),
		Dict{}, // pointer form only
		(*big.Int)(nil),
		(*Dict)(nil),
	} {
		_, _, err := EncodeArena([]any{v})
		require.Error(t, err, "%T should not encode", v)
		assert.Contains(t, err.Error(), "cannot encode")
	}
}

func TestEncodeRejectsBadDict(t *testing.T) {
	_, _, err := EncodeArena([]any{&Dict{Keys: []any{"a", "b"}, Values: []any{int64(1)}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 values")
}

func TestEncodeRejectsSelfReferentialValue(t *testing.T) {
	loop := []any{int64(1)}
	loop[0] = loop
	_, _, err := EncodeArena([]any{loop})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "self-referential")
}

// TestDecodeUnsupportedArms: every arm this client does not model is a typed
// error, so a caller can distinguish it from a nil value.
func TestDecodeUnsupportedArms(t *testing.T) {
	for _, tc := range []struct {
		kind string
		node *pb.MontyNode
	}{
		{"ellipsis", &pb.MontyNode{Kind: &pb.MontyNode_Ellipsis{Ellipsis: &pb.Unit{}}}},
		{"not_implemented", &pb.MontyNode{Kind: &pb.MontyNode_NotImplemented{NotImplemented: &pb.Unit{}}}},
		{"uuid", &pb.MontyNode{Kind: &pb.MontyNode_Uuid{Uuid: &pb.Uuid{Data: make([]byte, 16)}}}},
		{"named_tuple", &pb.MontyNode{Kind: &pb.MontyNode_NamedTuple{NamedTuple: &pb.NamedTupleNode{TypeName: "os.stat_result"}}}},
		{"date", &pb.MontyNode{Kind: &pb.MontyNode_Date{Date: &pb.Date{Year: 2024, Month: 1, Day: 1}}}},
		{"time", &pb.MontyNode{Kind: &pb.MontyNode_Time{Time: &pb.Time{Hour: 1}}}},
		{"datetime", &pb.MontyNode{Kind: &pb.MontyNode_Datetime{Datetime: &pb.DateTime{Year: 2024}}}},
		{"timedelta", &pb.MontyNode{Kind: &pb.MontyNode_Timedelta{Timedelta: &pb.TimeDelta{Days: 1}}}},
		{"timezone", &pb.MontyNode{Kind: &pb.MontyNode_Timezone{Timezone: &pb.TimeZone{OffsetSeconds: 3600}}}},
		{"exception", &pb.MontyNode{Kind: &pb.MontyNode_Exception{Exception: &pb.Exception{ExcType: "ValueError"}}}},
		{"type", &pb.MontyNode{Kind: &pb.MontyNode_Type{Type: &pb.Type{Name: "int"}}}},
		{"class_instance", &pb.MontyNode{Kind: &pb.MontyNode_ClassInstance{ClassInstance: &pb.ClassInstanceNode{}}}},
		{"function", &pb.MontyNode{Kind: &pb.MontyNode_Function{Function: &pb.Function{Name: "f"}}}},
		{"builtin_function", &pb.MontyNode{Kind: &pb.MontyNode_BuiltinFunction{BuiltinFunction: "len"}}},
		{"path", &pb.MontyNode{Kind: &pb.MontyNode_Path{Path: "/tmp/x"}}},
		{"file_handle", &pb.MontyNode{Kind: &pb.MontyNode_FileHandle{FileHandle: &pb.FileHandle{Path: "/tmp/x", Mode: "r"}}}},
		// Output-only arms: Monty itself rejects these as inputs.
		{"repr", &pb.MontyNode{Kind: &pb.MontyNode_Repr{Repr: "<object>"}}},
		{"cycle", &pb.MontyNode{Kind: &pb.MontyNode_Cycle{Cycle: "[...]"}}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			arena := &pb.Arena{NodeCount: 1, Nodes: []*pb.MontyNode{tc.node}}
			_, err := DecodeArena(arena, []uint32{0})
			require.Error(t, err)

			var uve *UnsupportedValueError
			require.ErrorAs(t, err, &uve)
			assert.Equal(t, tc.kind, uve.Node)
		})
	}
}

// TestDecodeReprAndCycle specifically pins the two output-only arms: a sandbox
// result is allowed to contain them, and this client still refuses them. Each
// arm gets its own arena because an arena is decoded whole, so one bad node
// would otherwise mask the next.
func TestDecodeReprAndCycle(t *testing.T) {
	for _, tc := range []struct {
		kind string
		node *pb.MontyNode
	}{
		{"repr", &pb.MontyNode{Kind: &pb.MontyNode_Repr{Repr: "<MyClass object>"}}},
		{"cycle", &pb.MontyNode{Kind: &pb.MontyNode_Cycle{Cycle: "[...]"}}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			arena := &pb.Arena{NodeCount: 1, Nodes: []*pb.MontyNode{tc.node}}
			_, err := DecodeArena(arena, []uint32{0})
			require.Error(t, err)
			var uve *UnsupportedValueError
			require.ErrorAs(t, err, &uve)
			assert.Equal(t, tc.kind, uve.Node)
		})
	}
}

// TestDecodeBadIndexes: the child process is untrusted, so a malformed frame
// is an error, never a panic.
func TestDecodeBadIndexes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		arena *pb.Arena
		roots []uint32
	}{
		{
			name:  "root out of range",
			arena: &pb.Arena{Nodes: []*pb.MontyNode{intN(1)}},
			roots: []uint32{1},
		},
		{
			name:  "root far out of range",
			arena: &pb.Arena{Nodes: []*pb.MontyNode{intN(1)}},
			roots: []uint32{1 << 31},
		},
		{
			name:  "root out of range on empty arena",
			arena: &pb.Arena{},
			roots: []uint32{0},
		},
		{
			name:  "list child out of range",
			arena: &pb.Arena{Nodes: []*pb.MontyNode{listN(5)}},
			roots: []uint32{0},
		},
		{
			name: "tuple child out of range",
			arena: &pb.Arena{Nodes: []*pb.MontyNode{
				{Kind: &pb.MontyNode_Tuple{Tuple: &pb.Indexes{Items: []uint32{9}}}},
			}},
			roots: []uint32{0},
		},
		{
			name: "set child out of range",
			arena: &pb.Arena{Nodes: []*pb.MontyNode{
				{Kind: &pb.MontyNode_Set{Set: &pb.Indexes{Items: []uint32{9}}}},
			}},
			roots: []uint32{0},
		},
		{
			name: "dict key out of range",
			arena: &pb.Arena{Nodes: []*pb.MontyNode{
				{Kind: &pb.MontyNode_Dict{Dict: &pb.NodePairs{Pairs: []*pb.NodePair{
					{Key: 7, Value: 0},
				}}}},
			}},
			roots: []uint32{0},
		},
		{
			name: "dict value out of range",
			arena: &pb.Arena{Nodes: []*pb.MontyNode{
				intN(1),
				{Kind: &pb.MontyNode_Dict{Dict: &pb.NodePairs{Pairs: []*pb.NodePair{
					{Key: 0, Value: 7},
				}}}},
			}},
			roots: []uint32{1},
		},
		{
			// A child index at or past the holder: post-order violated, and
			// a self-reference is the degenerate case of it.
			name:  "self cycle",
			arena: &pb.Arena{Nodes: []*pb.MontyNode{listN(0)}},
			roots: []uint32{0},
		},
		{
			name:  "forward reference",
			arena: &pb.Arena{Nodes: []*pb.MontyNode{listN(1), intN(1)}},
			roots: []uint32{0},
		},
		{
			name: "nested forward reference",
			arena: &pb.Arena{Nodes: []*pb.MontyNode{
				intN(1),
				listN(0, 2),
				intN(2),
			}},
			roots: []uint32{1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() { _, err = DecodeArena(tc.arena, tc.roots) })
			require.Error(t, err)
		})
	}
}

func TestDecodeMalformedNodes(t *testing.T) {
	t.Run("nil node", func(t *testing.T) {
		_, err := DecodeArena(&pb.Arena{Nodes: []*pb.MontyNode{nil}}, []uint32{0})
		require.Error(t, err)
	})
	t.Run("no kind set", func(t *testing.T) {
		_, err := DecodeArena(&pb.Arena{Nodes: []*pb.MontyNode{{}}}, []uint32{0})
		require.Error(t, err)
	})
	t.Run("unsupported arm anywhere in the arena, not just at a root", func(t *testing.T) {
		_, err := DecodeArena(&pb.Arena{Nodes: []*pb.MontyNode{
			intN(1),
			{Kind: &pb.MontyNode_Path{Path: "/x"}},
		}}, []uint32{0})
		require.Error(t, err)
	})
}

// TestDictOrderPreserved: Python dicts are insertion-ordered, which is exactly
// what a Go map cannot express.
func TestDictOrderPreserved(t *testing.T) {
	keys := []any{"zebra", "apple", int64(7), true, "apple"}
	values := []any{int64(1), "two", Tuple{int64(3)}, nil, "dup"}
	d := NewDict(keys, values)

	arena, roots, err := EncodeArena([]any{d})
	require.NoError(t, err)
	got, err := DecodeArena(arena, roots)
	require.NoError(t, err)
	require.Len(t, got, 1)

	back, ok := got[0].(*Dict)
	require.True(t, ok, "dicts decode to *Dict, not a Go map")
	assert.Equal(t, keys, back.Keys)
	assert.Equal(t, values, back.Values)
	assert.Equal(t, d.Len(), back.Len())
}
