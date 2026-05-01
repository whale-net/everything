#!/usr/bin/env python3
"""Generate chip_catalog.h from chips.yaml.

Usage: gen_chip_catalog.py <chips.yaml> <chip_catalog.h>
"""

import sys
import yaml

def to_cpp_name(chip_name: str) -> str:
    """Convert chip name to a valid C++ identifier prefix. e.g. SHT3x -> SHT3x"""
    return chip_name.replace("-", "_").replace(".", "_")

def main():
    if len(sys.argv) != 3:
        print(f"Usage: {sys.argv[0]} <chips.yaml> <chip_catalog.h>", file=sys.stderr)
        sys.exit(1)

    yaml_path, out_path = sys.argv[1], sys.argv[2]

    with open(yaml_path) as f:
        data = yaml.safe_load(f)

    lines = [
        "// Generated from chips.yaml — do not edit manually.",
        "// Re-generate: bazel build //firmware/sensor/catalog:chip_catalog_h",
        "#pragma once",
        "#include <cstdint>",
        "",
        "namespace firmware {",
        "namespace chip_addr {",
        "",
    ]

    for chip in data["chips"]:
        prefix = to_cpp_name(chip["name"])
        lines.append(f"// {chip['name']} — {chip['description']}")
        for addr in chip["addresses"]:
            label = addr.get("cpp_label", "Default" if addr.get("is_default") else "Alt")
            cfg   = addr.get("addr_config", "")
            dec   = addr["i2c_address"]
            hex_  = f"0x{dec:02X}"
            comment = f"  // {cfg}" if cfg else ""
            lines.append(
                f"constexpr uint8_t k{prefix}{label} = {hex_};{comment}"
            )
        lines.append("")

    lines += [
        "}  // namespace chip_addr",
        "}  // namespace firmware",
        "",
    ]

    with open(out_path, "w") as f:
        f.write("\n".join(lines))

    print(f"Generated {out_path}")

if __name__ == "__main__":
    main()
