package monty

// Arena <-> Go value codec.
//
// Monty flattens every Python value graph in a message into one `Arena`: a
// post-order node list where a container holds the indexes of its children and
// the carrying message names its roots by index. Two consequences shape this
// file:
//
//   - Decoding is a single forward pass. A node's children always have a lower
//     index than the node itself, so building values in index order needs no
//     fixup pass -- a container just reads back the slice of already-built
//     values.
//   - An object passed under two names is ONE node, so encoding deduplicates by
//     identity rather than by value: two references to the same slice, Tuple,
//     Set, FrozenSet, Dict or big.Int must share one index.
//
// Every index that crosses this boundary comes from the child process and is
// therefore untrusted: a bad frame is a returned error, never a panic, because
// the upstream Rust client treats malformed frames as fatal too.

import (
	"fmt"
	"math/big"
	"reflect"

	pb "github.com/whale-net/everything/libs/go/monty/protos"
)

// DecodeArena materialises the roots of an arena. nodes is the arena's node
// list; roots are indexes into it.
//
// A []uint32 rather than a single root because a Complete names one result
// while a FunctionCall names many (args and kwargs).
func DecodeArena(a *pb.Arena, roots []uint32) ([]any, error) {
	if a == nil {
		return nil, fmt.Errorf("monty: nil arena")
	}

	nodes := a.GetNodes()
	built := make([]any, 0, len(nodes))
	for i, n := range nodes {
		v, err := decodeNode(n, i, built)
		if err != nil {
			return nil, err
		}
		built = append(built, v)
	}

	out := make([]any, 0, len(roots))
	for i, r := range roots {
		if uint64(r) >= uint64(len(built)) {
			return nil, fmt.Errorf("monty: arena root %d names node %d, but the arena holds %d nodes", i, r, len(built))
		}
		out = append(out, built[r])
	}
	return out, nil
}

// decodeNode materialises one node. built holds every node decoded so far,
// which -- because the arena is post-order -- is exactly the set of indexes a
// child reference is allowed to name.
func decodeNode(n *pb.MontyNode, idx int, built []any) (any, error) {
	if n == nil {
		return nil, fmt.Errorf("monty: arena node %d is nil", idx)
	}
	switch k := n.GetKind().(type) {
	case *pb.MontyNode_None:
		return nil, nil
	case *pb.MontyNode_Boolean:
		return k.Boolean, nil
	case *pb.MontyNode_Int:
		return k.Int, nil
	case *pb.MontyNode_Bigint:
		return bigIntOf(bigFromPB(k.Bigint)), nil
	case *pb.MontyNode_Float:
		return k.Float, nil
	case *pb.MontyNode_Str:
		return k.Str, nil
	case *pb.MontyNode_Bytes:
		return k.Bytes, nil
	case *pb.MontyNode_List:
		items, err := childValues(idx, k.List.GetItems(), built)
		if err != nil {
			return nil, err
		}
		return items, nil
	case *pb.MontyNode_Tuple:
		items, err := childValues(idx, k.Tuple.GetItems(), built)
		if err != nil {
			return nil, err
		}
		return Tuple(items), nil
	case *pb.MontyNode_Set:
		items, err := childValues(idx, k.Set.GetItems(), built)
		if err != nil {
			return nil, err
		}
		return Set(items), nil
	case *pb.MontyNode_FrozenSet:
		items, err := childValues(idx, k.FrozenSet.GetItems(), built)
		if err != nil {
			return nil, err
		}
		return FrozenSet(items), nil
	case *pb.MontyNode_Dict:
		return decodeDict(idx, k.Dict.GetPairs(), built)

	// Arms this client does not model. A typed error is deliberate: a caller
	// must be able to tell "the sandbox returned something I don't represent"
	// from "the sandbox returned nil", rather than silently getting a lossy
	// approximation of a datetime or a class instance.
	case *pb.MontyNode_Ellipsis:
		return nil, unsupported("ellipsis")
	case *pb.MontyNode_NotImplemented:
		return nil, unsupported("not_implemented")
	case *pb.MontyNode_Uuid:
		return nil, unsupported("uuid")
	case *pb.MontyNode_NamedTuple:
		return nil, unsupported("named_tuple")
	case *pb.MontyNode_Date:
		return nil, unsupported("date")
	case *pb.MontyNode_Time:
		return nil, unsupported("time")
	case *pb.MontyNode_Datetime:
		return nil, unsupported("datetime")
	case *pb.MontyNode_Timedelta:
		return nil, unsupported("timedelta")
	case *pb.MontyNode_Timezone:
		return nil, unsupported("timezone")
	case *pb.MontyNode_Exception:
		return nil, unsupported("exception")
	case *pb.MontyNode_Type:
		return nil, unsupported("type")
	case *pb.MontyNode_ClassInstance:
		return nil, unsupported("class_instance")
	case *pb.MontyNode_Function:
		return nil, unsupported("function")
	case *pb.MontyNode_BuiltinFunction:
		return nil, unsupported("builtin_function")
	case *pb.MontyNode_Path:
		return nil, unsupported("path")
	case *pb.MontyNode_FileHandle:
		return nil, unsupported("file_handle")
	// repr and cycle are OUTPUT-ONLY in the protocol -- the child may emit
	// them inside a Complete value but rejects them as inputs, so a decoded
	// one is never something this client can send back.
	case *pb.MontyNode_Repr:
		return nil, unsupported("repr")
	case *pb.MontyNode_Cycle:
		return nil, unsupported("cycle")

	case nil:
		return nil, fmt.Errorf("monty: arena node %d sets no kind", idx)
	default:
		return nil, fmt.Errorf("monty: arena node %d has unhandled kind %T", idx, k)
	}
}

// childValues resolves a container's item indexes against the built prefix.
func childValues(holder int, items []uint32, built []any) ([]any, error) {
	out := make([]any, 0, len(items))
	for i, idx := range items {
		v, err := child(holder, idx, built)
		if err != nil {
			return nil, fmt.Errorf("monty: arena node %d: item %d: %w", holder, i, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// decodeDict builds a Dict from NodePairs, preserving the order they arrived
// in -- Python dicts are insertion-ordered and a Go map cannot express that.
func decodeDict(holder int, pairs []*pb.NodePair, built []any) (any, error) {
	d := &Dict{Keys: make([]any, 0, len(pairs)), Values: make([]any, 0, len(pairs))}
	for i, p := range pairs {
		k, err := child(holder, p.GetKey(), built)
		if err != nil {
			return nil, fmt.Errorf("monty: arena node %d: dict key %d: %w", holder, i, err)
		}
		v, err := child(holder, p.GetValue(), built)
		if err != nil {
			return nil, fmt.Errorf("monty: arena node %d: dict value %d: %w", holder, i, err)
		}
		d.Keys = append(d.Keys, k)
		d.Values = append(d.Values, v)
	}
	return d, nil
}

// child resolves one child index. Nodes are post-order, so an index at or past
// the built prefix is out of range, forward-referencing, or a self-reference --
// all three are a malformed frame, and all three error rather than panic.
func child(holder int, idx uint32, built []any) (any, error) {
	if uint64(idx) >= uint64(len(built)) {
		return nil, fmt.Errorf("child index %d is not a previously built node (node %d sees %d so far)", idx, holder, len(built))
	}
	return built[idx], nil
}

// bigFromPB turns the wire form (sign plus big-endian magnitude) into a
// big.Int. The int64 narrowing is decodeNode's job, via bigIntOf.
func bigFromPB(b *pb.BigInt) *big.Int {
	m := new(big.Int).SetBytes(b.GetMagnitude())
	if b.GetNegative() {
		m.Neg(m)
	}
	return m
}

// bigToPB is bigFromPB's inverse.
func bigToPB(i *big.Int) *pb.BigInt {
	return &pb.BigInt{
		Negative:  i.Sign() < 0,
		Magnitude: new(big.Int).Abs(i).Bytes(),
	}
}

// EncodeArena flattens values into one arena, returning it plus the root index
// of each value.
func EncodeArena(values []any) (*pb.Arena, []uint32, error) {
	e := &encoder{
		seen:       make(map[any]uint32),
		inProgress: make(map[any]bool),
	}
	roots := make([]uint32, 0, len(values))
	for _, v := range values {
		idx, err := e.encode(v)
		if err != nil {
			return nil, nil, err
		}
		roots = append(roots, idx)
	}
	return &pb.Arena{NodeCount: uint32(len(e.nodes)), Nodes: e.nodes}, roots, nil
}

// encoder accumulates the post-order node list plus the identity index used to
// collapse a repeated object onto a single node.
type encoder struct {
	nodes      []*pb.MontyNode
	seen       map[any]uint32 // identity key -> node index
	inProgress map[any]bool   // identity keys whose node is still being built
}

// encode appends v (and everything under it) and returns its index. Children
// are appended first, which is what makes the arena post-order.
func (e *encoder) encode(v any) (uint32, error) {
	key, dedup := identityKey(v)
	if dedup {
		if idx, ok := e.seen[key]; ok {
			return idx, nil
		}
		if e.inProgress[key] {
			// A self-referential slice or dict: the post-order invariant the
			// decoder relies on cannot be built for it.
			return 0, fmt.Errorf("monty: cannot encode a self-referential %T: the arena has no cycle arm", v)
		}
		e.inProgress[key] = true
	}

	n, err := e.node(v)
	if key != nil {
		delete(e.inProgress, key)
	}
	if err != nil {
		return 0, err
	}
	e.nodes = append(e.nodes, n)
	idx := uint32(len(e.nodes) - 1)
	if dedup {
		e.seen[key] = idx
	}
	return idx, nil
}

// node builds one node, recursing through e.encode for its children.
func (e *encoder) node(v any) (*pb.MontyNode, error) {
	switch t := v.(type) {
	case nil:
		return &pb.MontyNode{Kind: &pb.MontyNode_None{None: &pb.Unit{}}}, nil
	case bool:
		return &pb.MontyNode{Kind: &pb.MontyNode_Boolean{Boolean: t}}, nil

	// Go's untyped-ish integer spellings all narrow to the sint64 arm; an
	// integer too wide for int64 is a *big.Int, not a uint64 (which has no
	// lossless spelling and is rejected below).
	case int:
		return intNode(int64(t)), nil
	case int8:
		return intNode(int64(t)), nil
	case int16:
		return intNode(int64(t)), nil
	case int32:
		return intNode(int64(t)), nil
	case int64:
		return intNode(t), nil
	case uint:
		return intNode(int64(t)), nil
	case uint8:
		return intNode(int64(t)), nil
	case uint16:
		return intNode(int64(t)), nil
	case uint32:
		return intNode(int64(t)), nil

	case float32:
		return floatNode(float64(t)), nil
	case float64:
		return floatNode(t), nil
	case string:
		return &pb.MontyNode{Kind: &pb.MontyNode_Str{Str: t}}, nil
	case []byte:
		return &pb.MontyNode{Kind: &pb.MontyNode_Bytes{Bytes: t}}, nil

	case *big.Int:
		if t == nil {
			return nil, fmt.Errorf("monty: cannot encode a nil *big.Int")
		}
		// The schema splits int and bigint on width, so a big.Int that fits
		// goes down the sint64 arm and decodes back as int64.
		if t.IsInt64() {
			return intNode(t.Int64()), nil
		}
		return &pb.MontyNode{Kind: &pb.MontyNode_Bigint{Bigint: bigToPB(t)}}, nil

	case []any:
		items, err := e.items(t)
		if err != nil {
			return nil, err
		}
		return &pb.MontyNode{Kind: &pb.MontyNode_List{List: &pb.Indexes{Items: items}}}, nil
	case Tuple:
		items, err := e.items(t)
		if err != nil {
			return nil, err
		}
		return &pb.MontyNode{Kind: &pb.MontyNode_Tuple{Tuple: &pb.Indexes{Items: items}}}, nil
	case Set:
		items, err := e.items(t)
		if err != nil {
			return nil, err
		}
		return &pb.MontyNode{Kind: &pb.MontyNode_Set{Set: &pb.Indexes{Items: items}}}, nil
	case FrozenSet:
		items, err := e.items(t)
		if err != nil {
			return nil, err
		}
		return &pb.MontyNode{Kind: &pb.MontyNode_FrozenSet{FrozenSet: &pb.Indexes{Items: items}}}, nil
	case *Dict:
		if t == nil {
			return nil, fmt.Errorf("monty: cannot encode a nil *Dict")
		}
		if len(t.Keys) != len(t.Values) {
			return nil, fmt.Errorf("monty: cannot encode a *Dict with %d keys and %d values", len(t.Keys), len(t.Values))
		}
		pairs := make([]*pb.NodePair, 0, len(t.Keys))
		for i := range t.Keys {
			k, err := e.encode(t.Keys[i])
			if err != nil {
				return nil, fmt.Errorf("monty: dict key %d: %w", i, err)
			}
			val, err := e.encode(t.Values[i])
			if err != nil {
				return nil, fmt.Errorf("monty: dict value %d: %w", i, err)
			}
			pairs = append(pairs, &pb.NodePair{Key: k, Value: val})
		}
		return &pb.MontyNode{Kind: &pb.MontyNode_Dict{Dict: &pb.NodePairs{Pairs: pairs}}}, nil

	default:
		// A Go map cannot express Python's insertion order, so a map input is
		// a shape error rather than a value this client silently drops keys
		// from.
		if reflect.ValueOf(v).Kind() == reflect.Map {
			return nil, fmt.Errorf("monty: cannot encode %T as a sandbox value: a Go map has no insertion order, use *monty.Dict", v)
		}
		return nil, fmt.Errorf("monty: cannot encode %T as a sandbox value", v)
	}
}

// items encodes each element and returns the child indexes a container holds.
func (e *encoder) items(elems []any) ([]uint32, error) {
	out := make([]uint32, 0, len(elems))
	for i, el := range elems {
		idx, err := e.encode(el)
		if err != nil {
			return nil, fmt.Errorf("monty: item %d: %w", i, err)
		}
		out = append(out, idx)
	}
	return out, nil
}

func intNode(v int64) *pb.MontyNode {
	return &pb.MontyNode{Kind: &pb.MontyNode_Int{Int: v}}
}

func floatNode(v float64) *pb.MontyNode {
	return &pb.MontyNode{Kind: &pb.MontyNode_Float{Float: v}}
}

// sliceKey identifies a slice-backed value by its element type, data pointer
// and length, so two references to the same backing array -- the same Python
// list -- collapse onto one node. Only ever compared, never dereferenced, so
// holding a uintptr here is safe for the lifetime of one encode.
type sliceKey struct {
	typ reflect.Type
	ptr uintptr
	len int
}

// identityKey returns the deduplication key for v, if the arena deduplicates v
// at all. Pointers key on themselves; the slice-backed containers key on their
// backing array. Scalars deliberately do NOT dedup: two equal ints are two
// Python objects, and collapsing them would be value identity, not the object
// identity the arena is for.
func identityKey(v any) (any, bool) {
	switch v.(type) {
	case *Dict, *big.Int:
		return v, true
	case []any, Tuple, Set, FrozenSet, []byte:
		rv := reflect.ValueOf(v)
		return sliceKey{typ: rv.Type(), ptr: rv.Pointer(), len: rv.Len()}, true
	}
	return nil, false
}
