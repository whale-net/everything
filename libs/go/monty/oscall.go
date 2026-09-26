package monty

import (
	"encoding/hex"
	"fmt"

	pb "github.com/whale-net/everything/libs/go/monty/protos"
)

// osCallName renders an OS suspension as an operation name plus the payload
// the call carries, so describeOS can build a Call without a thirty-arm
// switch of its own.
//
// Monty services no OS call itself: a mount is consulted by the host, not by
// the worker. A host that answers ErrNotFound therefore gets the documented
// default for the operation -- PermissionError naming the path for
// filesystem calls, RuntimeError for the rest.
func osCallName(oc *pb.OsCall) (string, any, error) {
	switch c := oc.GetCall().(type) {
	// ---- filesystem, one virtual path each ----
	case *pb.OsCall_Exists:
		return "exists", c.Exists, nil
	case *pb.OsCall_IsFile:
		return "is_file", c.IsFile, nil
	case *pb.OsCall_IsDir:
		return "is_dir", c.IsDir, nil
	case *pb.OsCall_IsSymlink:
		return "is_symlink", c.IsSymlink, nil
	case *pb.OsCall_ReadText:
		return "read_text", c.ReadText, nil
	case *pb.OsCall_ReadBytes:
		return "read_bytes", c.ReadBytes, nil
	case *pb.OsCall_Stat:
		return "stat", c.Stat, nil
	case *pb.OsCall_Iterdir:
		return "iterdir", c.Iterdir, nil
	case *pb.OsCall_Resolve:
		return "resolve", c.Resolve, nil
	case *pb.OsCall_Absolute:
		return "absolute", c.Absolute, nil
	case *pb.OsCall_Unlink:
		return "unlink", c.Unlink, nil
	case *pb.OsCall_Rmdir:
		return "rmdir", c.Rmdir, nil

	// ---- filesystem, structured payloads ----
	case *pb.OsCall_WriteText:
		return "write_text", c.WriteText, nil
	case *pb.OsCall_AppendText:
		return "append_text", c.AppendText, nil
	case *pb.OsCall_WriteBytes:
		return "write_bytes", c.WriteBytes, nil
	case *pb.OsCall_AppendBytes:
		return "append_bytes", c.AppendBytes, nil
	case *pb.OsCall_Open_:
		return "open", c.Open, nil
	case *pb.OsCall_Mkdir_:
		return "mkdir", c.Mkdir, nil
	case *pb.OsCall_Rename_:
		return "rename", c.Rename, nil

	// ---- clock, environment, entropy ----
	case *pb.OsCall_Getenv_:
		return "getenv", c.Getenv, nil
	case *pb.OsCall_GetEnviron:
		return "get_environ", nil, nil
	case *pb.OsCall_DateToday:
		return "date_today", nil, nil
	case *pb.OsCall_DateTimeNow_:
		return "datetime_now", c.DateTimeNow, nil
	case *pb.OsCall_Urandom_:
		return "urandom", c.Urandom, nil
	case *pb.OsCall_Time:
		return "time", c.Time, nil
	case *pb.OsCall_Sleep_:
		return "sleep", c.Sleep, nil
	case *pb.OsCall_AsyncSleep_:
		return "async_sleep", c.AsyncSleep, nil
	case *pb.OsCall_SystemSleep:
		return "system_sleep", c.SystemSleep, nil
	case *pb.OsCall_AsyncSystemSleep:
		return "async_system_sleep", c.AsyncSystemSleep, nil

	default:
		return "", nil, fmt.Errorf("%w: OS call carried no known operation", ErrProtocol)
	}
}

// decodeRoot materialises a single arena root, the shape every turn-ender
// and every resume uses.
func decodeRoot(arena *pb.Arena, root uint32) (any, error) {
	values, err := DecodeArena(arena, []uint32{root})
	if err != nil {
		return nil, err
	}
	if len(values) != 1 {
		return nil, fmt.Errorf("%w: expected one decoded root, got %d", ErrProtocol, len(values))
	}
	return values[0], nil
}

// uuidString renders a host-object identity. A host-backed class instance or
// class type is addressed by a uuid the host generated, and only the host
// knows what it refers to.
func uuidString(u *pb.Uuid) string {
	if u == nil || len(u.GetData()) == 0 {
		return ""
	}
	return hex.EncodeToString(u.GetData())
}

// sourceRange renders a call site as "file:start-end" for host error
// messages. Returns "" when the worker did not send a position.
func sourceRange(sr *pb.SourceRange) string {
	if sr == nil {
		return ""
	}
	name := sr.GetFilename()
	if name == "" {
		name = "<feed>"
	}
	return fmt.Sprintf("%s:%d-%d", name, sr.GetStart(), sr.GetEnd())
}

func strPtr(s string) *string { return &s }
