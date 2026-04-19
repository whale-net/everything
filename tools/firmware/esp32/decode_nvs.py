#!/usr/bin/env python3
"""Decode all pages of an ESP32 NVS partition binary and verify CRCs.

Usage:
    python3 decode_nvs.py [nvs.bin]       # default: /tmp/nvs_readback.bin

Prints per-page summary and per-entry details. Reports CRC mismatches.
"""

import struct
import sys
from binascii import crc32 as _bcrc32

_PAGE_SIZE  = 4096
_ENTRY_SIZE = 32

_STATE_NAMES = {
    0xFFFFFFFF: "ERASED",
    0xFFFFFFFE: "ACTIVE",
    0xFFFFFFFC: "FULL",
    0x00000000: "CORRUPT",
}

def _crc32(data: bytes) -> int:
    return _bcrc32(data, 0xFFFFFFFF) & 0xFFFFFFFF


def decode_page(page: bytes, page_idx: int) -> None:
    assert len(page) == _PAGE_SIZE

    state,  = struct.unpack_from("<I", page, 0)
    seq_no, = struct.unpack_from("<I", page, 4)
    ver     = page[8]
    hdr_crc,= struct.unpack_from("<I", page, 28)
    computed_hdr_crc = _crc32(page[4:28])
    hdr_ok = hdr_crc == computed_hdr_crc

    state_name = _STATE_NAMES.get(state, f"0x{state:08x}")
    is_erased  = page == b"\xff" * _PAGE_SIZE

    print(f"\n=== Page {page_idx} (flash offset 0x{page_idx * _PAGE_SIZE + 0x9000:05x}) ===")
    print(f"  state:   0x{state:08x} ({state_name})")
    if is_erased:
        print("  (all 0xFF — erased)")
        return

    print(f"  seq_no:  {seq_no}")
    print(f"  ver:     0x{ver:02x}  {'OK' if ver == 0xFE else '*** UNEXPECTED (want 0xFE) ***'}")
    print(f"  hdr_crc: stored=0x{hdr_crc:08x}  computed=0x{computed_hdr_crc:08x}  {'OK' if hdr_ok else '*** MISMATCH ***'}")

    if not hdr_ok:
        print("  (skipping entry decode — corrupt header)")
        return

    bitmap = page[32:64]
    print(f"  bitmap[0:8]: {bitmap[:8].hex()}")

    entries_start = 64
    all_ff = b"\xff" * _ENTRY_SIZE
    i = 0
    while i < 126:
        off   = entries_start + i * _ENTRY_SIZE
        entry = page[off:off + _ENTRY_SIZE]
        if entry == all_ff:
            print(f"  Entry {i:3d}: EMPTY")
            break

        ns    = entry[0]
        typ   = entry[1]
        span  = entry[2]
        chunk = entry[3]
        stored_crc, = struct.unpack_from("<I", entry, 4)
        key   = entry[8:24]
        data  = entry[24:32]

        crc_input    = bytes([ns, typ, span, chunk]) + key + data
        computed_crc = _crc32(crc_input)
        crc_ok       = stored_crc == computed_crc

        key_str = key.rstrip(b"\x00").decode("ascii", errors="replace")

        # Raw data entries (span > 1 continuation) are not independently CRC'd
        is_data_chunk = (ns == 0xFF or (typ not in (0x01, 0x11, 0x12, 0x13, 0x14,
                                                      0x15, 0x16, 0x17, 0x18, 0x21,
                                                      0x41, 0x42)))

        if typ == 0x01:  # namespace entry
            print(f"  Entry {i:3d}: NAMESPACE ns_idx={data[0]} name='{key_str}' crc={'OK' if crc_ok else 'MISMATCH'}")
        elif typ == 0x21:  # NVS_TYPE_STR
            str_len,  = struct.unpack_from("<H", data, 0)
            str_crc16,= struct.unpack_from("<H", data, 2)
            data_start = off + _ENTRY_SIZE
            raw = page[data_start:data_start + (span - 1) * _ENTRY_SIZE]
            value = raw[:max(0, str_len - 1)].decode("utf-8", errors="replace")
            print(f"  Entry {i:3d}: STR  ns={ns} key='{key_str}' len={str_len} "
                  f"crc={'OK' if crc_ok else 'MISMATCH'} value={value!r}")
            if not crc_ok:
                print(f"             crc_input: {crc_input.hex()}")
            # Skip continuation entries
            i += span
            continue
        else:
            marker = "(data chunk)" if is_data_chunk else ""
            print(f"  Entry {i:3d}: typ=0x{typ:02x} ns={ns} span={span} key='{key_str}' "
                  f"data={data.hex()} crc={'OK' if crc_ok else 'MISMATCH'} {marker}")

        i += 1


def main() -> None:
    path = sys.argv[1] if len(sys.argv) > 1 else "/tmp/nvs_readback.bin"
    with open(path, "rb") as f:
        data = f.read()

    n_pages = len(data) // _PAGE_SIZE
    print(f"File: {path}  ({len(data)} bytes, {n_pages} pages)")

    for idx in range(n_pages):
        page = data[idx * _PAGE_SIZE:(idx + 1) * _PAGE_SIZE]
        decode_page(page, idx)

    print()


if __name__ == "__main__":
    main()
