#!/usr/bin/env python3
"""Provision a LeafLab device with WiFi credentials stored in NVS.

Generates a minimal NVS partition image containing the WiFi SSID and
password, then flashes it to the ESP32's NVS partition at address 0x9000.
Credentials survive firmware reflashes and are never baked into the
application binary.

Usage (via Bazel):
    bazel run //leaflab/sensorboard:provision -- /dev/ttyUSB0 "MySSID" "MyPass"

Usage (direct):
    python3 provision.py --esptool /path/to/esptool_wrapper \\
        --port /dev/ttyUSB0 --ssid "MySSID" --password "MyPass"

The firmware reads credentials from NVS namespace "creds", keys "wifi_ssid"
and "wifi_pass", using the Arduino Preferences library.
"""

import argparse
import struct
import subprocess
import sys
import tempfile
import os
from binascii import crc32 as _binascii_crc32


# ── NVS binary generator ─────────────────────────────────────────────────────
# Generates a single 4096-byte NVS page containing one namespace and any
# number of string key-value entries.
#
# NVS page layout (ESP-IDF v5 format):
#   [Header: 32 B][Bitmap: 32 B][Entries: 126 × 32 B]
#
# Entry layout:
#   [ns_idx:1][type:1][span:1][chunk:1][crc32:4][key:16][data:8]
#   CRC32 covers ns_idx+type+span+chunk (4 B) + key (16 B) + data (8 B) = 28 B.
#
# String entry (type 0x21 = NVS_TYPE_STR):
#   data field: [len:2][crc16:2][0xFF:4]
#   Followed by (span-1) raw data entries, each 32 bytes, padded with 0xFF.
#   CRC16 is CRC-16/CCITT of the null-terminated string bytes.

_PAGE_SIZE  = 4096
_ENTRY_SIZE = 32
_MAX_ENTRIES = 126


def _crc32(data: bytes) -> int:
    return _binascii_crc32(data, 0xFFFFFFFF) ^ 0xFFFFFFFF


def _crc16_ccitt(data: bytes) -> int:
    """CRC-16/CCITT: poly 0x1021, init 0xFFFF."""
    crc = 0xFFFF
    for b in data:
        crc ^= b << 8
        for _ in range(8):
            crc = ((crc << 1) ^ 0x1021) if (crc & 0x8000) else (crc << 1)
            crc &= 0xFFFF
    return crc


def _page_header() -> bytes:
    """32-byte page header. CRC32 covers bytes 4-27 (seq+version+reserved)."""
    state  = 0xFFFFFFFE  # active
    seq_no = 0
    ver    = 0xFF
    pre_crc = struct.pack("<IB", seq_no, ver) + b"\xFF" * 19  # 24 bytes
    crc = _crc32(pre_crc)
    return struct.pack("<I", state) + pre_crc + struct.pack("<I", crc)


def _bitmap(n_written: int) -> bytes:
    """32-byte entry state bitmap. Written entry i → clear bit (2*i)."""
    bits = bytearray(b"\xFF" * 32)
    for i in range(n_written):
        byte_idx = (2 * i) // 8
        bit_idx  = (2 * i) % 8
        bits[byte_idx] &= ~(1 << bit_idx)
    return bytes(bits)


def _pack_entry(ns: int, typ: int, span: int, key16: bytes, data8: bytes) -> bytes:
    """Pack a 32-byte NVS entry. CRC covers meta (4 B) + key (16 B) + data (8 B)."""
    chunk = 0xFF
    crc = _crc32(bytes([ns, typ, span, chunk]) + key16 + data8)
    return bytes([ns, typ, span, chunk]) + struct.pack("<I", crc) + key16 + data8


def _ns_entry(name: str, ns_idx: int) -> bytes:
    """Namespace declaration entry (type 0x01)."""
    key  = name.encode()[:15].ljust(16, b"\x00")
    data = bytes([ns_idx]) + b"\xFF" * 7
    return _pack_entry(0, 0x01, 1, key, data)


def _str_entries(ns: int, key_str: str, value: str) -> list:
    """Header entry + data entries for a NUL-terminated string (type 0x21)."""
    key  = key_str.encode()[:15].ljust(16, b"\x00")
    blob = value.encode("utf-8") + b"\x00"          # NVS stores NUL-terminated
    n_data = (len(blob) + _ENTRY_SIZE - 1) // _ENTRY_SIZE
    span = 1 + n_data
    crc16 = _crc16_ccitt(blob)
    data = struct.pack("<HH", len(blob), crc16) + b"\xFF" * 4
    header = _pack_entry(ns, 0x21, span, key, data)
    padded = blob.ljust(n_data * _ENTRY_SIZE, b"\xFF")
    chunks = [padded[i * _ENTRY_SIZE:(i + 1) * _ENTRY_SIZE] for i in range(n_data)]
    return [header] + chunks


def make_nvs_page(namespace: str, fields: dict) -> bytes:
    """Generate a 4096-byte NVS partition image with one namespace."""
    entries = [_ns_entry(namespace, 1)]
    for k, v in fields.items():
        entries.extend(_str_entries(1, k, v))
    n = len(entries)
    if n > _MAX_ENTRIES:
        raise ValueError(f"Too many NVS entries: {n} (max {_MAX_ENTRIES})")
    header = _page_header()
    bitmap = _bitmap(n)
    body   = b"".join(entries).ljust(_MAX_ENTRIES * _ENTRY_SIZE, b"\xFF")
    page   = header + bitmap + body
    assert len(page) == _PAGE_SIZE, f"Bad page size: {len(page)}"
    return page


# ── Flash helper ─────────────────────────────────────────────────────────────

def flash_nvs(esptool: str, port: str, nvs_bin: str) -> None:
    cmd = [
        sys.executable, esptool,
        "--port", port,
        "--chip", "esp32",
        "--baud", "921600",
        "--before", "default-reset",
        "--after", "hard-reset",
        "write-flash",
        "--flash-mode", "dio",
        "--flash-freq", "80m",
        "--flash-size", "4MB",
        "0x9000", nvs_bin,
    ]
    print("$ " + " ".join(cmd))
    subprocess.check_call(cmd)


# ── Main ─────────────────────────────────────────────────────────────────────

def main() -> None:
    p = argparse.ArgumentParser(description="Provision ESP32 WiFi credentials via NVS")
    p.add_argument("port",     help="Serial port, e.g. /dev/ttyUSB0")
    p.add_argument("ssid",     help="WiFi network name")
    p.add_argument("password", help="WiFi password")
    p.add_argument("--esptool", required=True,
                   help="Path to esptool wrapper binary")
    args = p.parse_args()

    print(f"Provisioning {args.port} with SSID='{args.ssid}'")

    page = make_nvs_page("creds", {
        "wifi_ssid": args.ssid,
        "wifi_pass": args.password,
    })

    with tempfile.NamedTemporaryFile(suffix=".bin", delete=False) as f:
        f.write(page)
        tmp_path = f.name

    try:
        flash_nvs(args.esptool, args.port, tmp_path)
        print(f"\nProvisioned. Flash firmware next:")
        print(f"  bazel run //leaflab/sensorboard:flash -- {args.port}")
    finally:
        os.unlink(tmp_path)


if __name__ == "__main__":
    main()
