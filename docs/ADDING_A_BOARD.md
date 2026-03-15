# Adding a New Board

This guide covers adding a new microcontroller board to the firmware infrastructure.
The system supports any board via three abstractions:

1. **Platform constraints** — Bazel's type system for hardware targets
2. **Pin abstraction** — logical pin names that map to board-specific GPIO numbers
3. **Universal flash** — a single `bazel run :flash -- <port>` command across all boards

---

## Architecture

```
Application (board-agnostic C++)
    #include "board_pins.h"        ← board::kLed, board::kSda, …
    |
    ↓
//tools/firmware:board_pins        ← alias, resolves via select()
    |
    ├── //tools/firmware/esp32:elegoo_esp32_pins  (when --config=esp32)
    ├── //tools/firmware/pico:pico_w_pins         (future)
    └── //tools/firmware:no_board_pins            (host builds)

esp32_firmware() / pico_firmware() macro
    → {name}_lib   cc_library  (your sketch + board_pins)
    → {name}_elf   cc_binary   (ELF for the target CPU)
    → {name}_bin   genrule     (flashable image)
    → :flash       sh_binary   (bazel run :flash -- /dev/ttyUSB0)
         |
         └── tools/firmware/flash/flash.py  (dispatches by tool)
                 ├── esptool  (ESP32)
                 ├── avrdude  (AVR)
                 ├── picotool (RP2040)
                 └── openocd  (ARM/JTAG)
```

Key files:

| File | Purpose |
|------|---------|
| `tools/firmware/BUILD.bazel` | CPU + board constraint_settings; `board_pins` alias |
| `tools/firmware/boards.bzl` | `firmware_board()` macro |
| `tools/firmware/pins.bzl` | `board_pins()` macro — generates `board_pins.h` |
| `tools/firmware/flash.bzl` | `flash_firmware()` macro — generates the flash wrapper |
| `tools/firmware/flash/flash.py` | Universal flash driver |
| `tools/firmware/<family>/flash_config.bzl` | Board-specific flash parameters (Starlark struct) |
| `tools/bazel/esp32.bzl` | `esp32_firmware()` — the user-facing macro for ESP32 |

---

## Case A: New board, same CPU family (another ESP32)

Example: **HiLetgo NodeMCU-32S** or any generic DevKit v1 clone.

These boards use the same Xtensa LX6 core, the same Arduino ESP32 1.0.6 SDK,
and usually the same flash geometry. The only difference is pin layout.

### Step 1 — Register the board identity

```starlark
# tools/firmware/BUILD.bazel
constraint_value(
    name = "board_hiletgo_esp32",
    constraint_setting = ":board",
)
```

### Step 2 — Declare the board platform and pins

Create `tools/firmware/esp32/BUILD.bazel` additions (or a new subdir if you
prefer isolation):

```starlark
# tools/firmware/esp32/BUILD.bazel
firmware_board(
    name = "hiletgo_esp32",
    cpu_constraint = "//tools/firmware:cpu_xtensa",
)

config_setting(
    name = "is_hiletgo_esp32",
    constraint_values = ["//tools/firmware:board_hiletgo_esp32"],
)

board_pins(
    name = "hiletgo_esp32_pins",
    board_name = "hiletgo_esp32",
    pins = {
        "Led": 2,    # onboard LED
        "Sda": 21,
        "Scl": 22,
        # add whatever logical names your firmware needs
    },
)
```

### Step 3 — Register the pins alias

```starlark
# tools/firmware/BUILD.bazel  — add to the existing select()
alias(
    name = "board_pins",
    actual = select({
        "//tools/firmware/esp32:is_elegoo_esp32":  "//tools/firmware/esp32:elegoo_esp32_pins",
        "//tools/firmware/esp32:is_hiletgo_esp32": "//tools/firmware/esp32:hiletgo_esp32_pins",
        "//conditions:default": ":no_board_pins",
    }),
)
```

### Step 4 — Add a .bazelrc config (optional)

```
# .bazelrc
build:hiletgo_esp32 --platforms=//tools/firmware/esp32:hiletgo_esp32
build:hiletgo_esp32 --@pigweed//pw_log:backend=@pigweed//pw_log_basic
```

Then:
```bash
bazel build //demo/blink:blink_bin --config=hiletgo_esp32
bazel run   //demo/blink:flash    --config=hiletgo_esp32 -- /dev/ttyUSB0
```

If the board uses QIO flash mode instead of DIO, pass `ESP32_QIO_80M` from
`tools/firmware/esp32/flash_config.bzl` to `esp32_firmware()`:

```starlark
load("//tools/firmware/esp32:flash_config.bzl", "ESP32_QIO_80M")

esp32_firmware(
    name = "blink",
    srcs  = ["blink.cc"],
    deps  = ["@pigweed//pw_log"],
    flash_config = ESP32_QIO_80M,
)
```

---

## Case B: New CPU family (e.g., Raspberry Pi Pico / RP2040)

This involves more work but the skeleton is already in place.

### Step 1 — CPU constraint (if new architecture)

```starlark
# tools/firmware/BUILD.bazel  — already has cpu_armv6m for RP2040
constraint_value(
    name = "cpu_armv6m",
    constraint_setting = ":cpu_family",
)
```

### Step 2 — Board identity + platform

```starlark
# tools/firmware/BUILD.bazel
constraint_value(
    name = "board_pico_w",
    constraint_setting = ":board",
)
```

```starlark
# tools/firmware/pico/BUILD.bazel  (new directory)
load("//tools/firmware:boards.bzl", "firmware_board")
load("//tools/firmware:pins.bzl",   "board_pins")

firmware_board(
    name = "pico_w",
    cpu_constraint = "//tools/firmware:cpu_armv6m",
)

config_setting(
    name = "is_pico_w",
    constraint_values = ["//tools/firmware:board_pico_w"],
)

board_pins(
    name = "pico_w_pins",
    board_name = "pico_w",
    pins = {
        "Led": 25,   # onboard LED on Pico W is routed via CYW43 but GPIO 25 on base Pico
        "Sda":  4,
        "Scl":  5,
        "Uart0Tx":  0,
        "Uart0Rx":  1,
    },
)
```

### Step 3 — Toolchain

For ARM Cortex-M, **Pigweed provides the toolchain** via
`@pigweed//pw_toolchain:arm_gcc_cortex_m0`. Add to MODULE.bazel:

```starlark
# Already have pigweed via git_override — just use it:
register_toolchains("//tools/firmware/pico:pico_toolchain")
```

```starlark
# tools/firmware/pico/BUILD.bazel
toolchain(
    name = "pico_toolchain",
    exec_compatible_with  = ["@platforms//os:linux", "@platforms//cpu:x86_64"],
    target_compatible_with = ["@platforms//os:none", "//tools/firmware:cpu_armv6m"],
    toolchain      = "@pigweed//pw_toolchain:arm_gcc_cortex_m0",
    toolchain_type = "@rules_cc//cc:toolchain_type",
)
```

If Pigweed's pre-built ARM toolchain doesn't match your target, provide your
own `cc_toolchain` + `cc_toolchain_config`, following the same pattern as
`tools/firmware/esp32/cc_toolchain_config.bzl`.

### Step 4 — Platform SDK (pico-sdk)

Add pico-sdk to MODULE.bazel as an `http_archive`, then write a `BUILD.bazel`
wrapping it as a `cc_library`. Follow the same pattern as
`tools/firmware/esp32/arduino_core.BUILD`.

**Critical**: the Pico SDK's entry point (`main()` calling `setup()`/`loop()`)
must be in a `filegroup`, not a `cc_library`, for the same reason as the
Arduino `main.cpp` footgun — see the toolchain README for details.

### Step 5 — Flash config

```starlark
# tools/firmware/pico/flash_config.bzl
PICO_W = struct(
    tool = "picotool",
    app_offset = "0x10000000",  # RP2040 flash base
    pre_segments = [],           # picotool handles the UF2 format internally
    pre_segment_labels = [],
)
```

### Step 6 — User macro

```starlark
# tools/bazel/pico.bzl
load("//tools/firmware:flash.bzl", "flash_firmware")
load("//tools/firmware/pico:flash_config.bzl", "PICO_W")

PICO_COMPAT = ["@platforms//os:none", "//tools/firmware:cpu_armv6m"]

def pico_firmware(name, srcs, deps = [], flash_config = None, **kwargs):
    if flash_config == None:
        flash_config = PICO_W
    cc_library(
        name = name + "_lib",
        srcs = srcs,
        target_compatible_with = PICO_COMPAT,
        deps = deps + [
            "@pico_sdk//:pico_stdlib",
            "//tools/firmware:board_pins",
        ],
        **kwargs
    )
    # ... cc_binary, genrule for uf2 conversion ...
    flash_firmware(name = "flash", firmware_name = name, board_config = flash_config)
```

### Step 7 — .bazelrc

```
build:pico_w --platforms=//tools/firmware/pico:pico_w
build:pico_w --@pigweed//pw_log:backend=@pigweed//pw_log_basic
```

---

## Pin Abstraction

### Declaring pins

Pins are declared once per board in its BUILD.bazel using `board_pins()`:

```starlark
load("//tools/firmware:pins.bzl", "board_pins")

board_pins(
    name = "my_board_pins",
    board_name = "my_board",
    pins = {
        "Led":      2,   # PascalCase logical name → GPIO number
        "SensorCs": 5,
        "Sda":     21,
        "Scl":     22,
    },
)
```

`board_pins()` generates `board_pins.h` in the package's genfiles directory
and wraps it in a `cc_library` with `includes = ["."]` so it's reachable as:

```cpp
#include "board_pins.h"
```

### Using pins in firmware code

```cpp
#include "board_pins.h"   // dep: //tools/firmware:board_pins

void setup() {
    pinMode(board::kLed, OUTPUT);       // C++ typed constant
    Wire.begin(board::kSda, board::kScl);
}

// C macros also available (for C files or legacy code):
// BOARD_PIN_LED, BOARD_PIN_SDA, BOARD_PIN_SCL
```

The dependency is `//tools/firmware:board_pins` — the `alias()` in
`tools/firmware/BUILD.bazel` that resolves to the correct board at build time.
Host builds (tests) get an empty `no_board_pins` library.

### Adding a new logical pin name

If your firmware needs a pin name not yet in any board's declaration:

1. Add the name to every board's `board_pins()` call in its BUILD.bazel.
2. Use `0` or `-1` for boards that don't have that peripheral (and guard with
   `#if BOARD_PIN_SENSOR_CS >= 0` in C++ if needed).
3. Reference it via `board::kSensorCs` in the firmware.

---

## Flash Infrastructure

### How it works

`esp32_firmware()` (and future `pico_firmware()`) automatically generates a
`:flash` target via `flash_firmware()` in `tools/firmware/flash.bzl`.

```
flash_firmware()
    ↓ write_file()
  wrapper.sh  (generated per target, baked-in runfile paths)
    ↓ rlocation
  flash.py  (universal driver, receives resolved absolute paths)
    ↓
  esptool / avrdude / picotool / openocd
```

### Flashing

```bash
# Standard usage
bazel run //demo/blink:flash -- /dev/ttyUSB0

# Different board config (if esp32_firmware sets flash_config explicitly):
bazel run //demo/blink:flash -- /dev/ttyACM0
```

The port (`/dev/ttyUSB0`) is the only runtime argument. Everything else —
chip type, baud rate, bootloader path, partition table, app offset — is baked
into the generated wrapper at build time from the `board_config` struct.

### Board flash configs

Flash parameters live in `tools/firmware/<family>/flash_config.bzl` as
Starlark structs:

```starlark
# tools/firmware/esp32/flash_config.bzl
ESP32_DIO_80M = struct(
    tool = "esptool",
    chip = "esp32",
    baud = 921600,
    write_flash_args = "--flash_mode dio --flash_freq 80m --flash_size 4MB",
    app_offset = "0x10000",
    pre_segments = [
        ("0x1000", "arduino_esp32/tools/sdk/bin/bootloader_dio_80m.bin"),
        ("0x8000", "arduino_esp32/tools/partitions/default.bin"),
    ],
    pre_segment_labels = [
        "@arduino_esp32//:bootloader",
        "@arduino_esp32//:partitions",
    ],
    esptool = "//tools/firmware/esp32:esptool_wrapper",
    esptool_runfile = "_main/tools/firmware/esp32/esptool_wrapper",
)
```

To add a new board with a different flash geometry (e.g. 8 MB, QIO mode):

```starlark
MY_BOARD_FLASH = struct(
    tool = "esptool",
    chip = "esp32",
    baud = 921600,
    write_flash_args = "--flash_mode qio --flash_freq 80m --flash_size 8MB",
    app_offset = "0x10000",
    pre_segments = [
        ("0x1000", "arduino_esp32/tools/sdk/bin/bootloader_qio_80m.bin"),
        ("0x8000", "arduino_esp32/tools/partitions/default.bin"),
    ],
    pre_segment_labels = [
        "@arduino_esp32//:bootloader",
        "@arduino_esp32//:partitions",
    ],
    esptool = "//tools/firmware/esp32:esptool_wrapper",
    esptool_runfile = "_main/tools/firmware/esp32/esptool_wrapper",
)
```

Pass it to `esp32_firmware()`:

```starlark
load("//tools/bazel:esp32.bzl", "esp32_firmware")
load("//tools/firmware/esp32:flash_config.bzl", "MY_BOARD_FLASH")

esp32_firmware(
    name = "my_app",
    srcs  = ["my_app.cc"],
    flash_config = MY_BOARD_FLASH,
)
```

### Supporting a new flash tool

Add a new branch to `tools/firmware/flash/flash.py`:

```python
def flash_myboard(args, segments):
    _, app_path = segments[-1]
    cmd = ["myflasher", "--target", app_path, "--port", args.port]
    subprocess.check_call(cmd)

dispatch = {
    "esptool":  flash_esptool,
    "avrdude":  flash_avrdude,
    "picotool": flash_picotool,
    "openocd":  flash_openocd,
    "myflasher": flash_myboard,   # ← add here
}
```

Add a new branch to `flash_firmware()` in `tools/firmware/flash.bzl` to
generate the right command-line arguments in the wrapper script.

---

## Checklist: Adding Any Board

```
□ tools/firmware/BUILD.bazel
    □ constraint_value for board identity (always)
    □ constraint_value for cpu_family (only if new CPU)
    □ Add board to board_pins alias select()

□ tools/firmware/<family>/BUILD.bazel
    □ firmware_board()
    □ config_setting()
    □ board_pins() with all logical pins your firmware uses

□ tools/firmware/<family>/flash_config.bzl
    □ Flash config struct (tool, chip, segments, offsets)

□ tools/firmware/<family>/  (new CPU family only)
    □ cc_toolchain_config.bzl  (or reuse Pigweed's pw_toolchain)
    □ xtensa_toolchain.BUILD / pico_sdk.BUILD
    □ MODULE.bazel: http_archive + register_toolchains

□ tools/bazel/<family>.bzl  (new CPU family only)
    □ <family>_firmware() macro wrapping cc_library + cc_binary + flash_firmware()

□ .bazelrc
    □ build:<board> --platforms=//tools/firmware/<family>:<board>
    □ build:<board> --@pigweed//pw_log:backend=...

□ docs/ADDING_A_BOARD.md
    □ Update this document with the new board entry
```
