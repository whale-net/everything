# Firmware Build Infrastructure

Hermetic embedded firmware development inside the Bazel monorepo.
The core idea: **declare a board once, and any firmware that targets it compiles and flashes correctly** — no per-developer toolchain setup required.

For the application-layer libraries (sensor abstraction, MQTT writer, network state machine) see [`firmware/README.md`](../../firmware/README.md).

---

## Architecture

```
firmware/          ← application logic (board-agnostic, host-testable)
  sensor/          ISensor interface + FakeSensor / RecordingSensor mocks
  mqtt/            MQTTWriter (zero-allocation) + IPublisher + FakePublisher
  network/         NetworkManager state machine (Wi-Fi + MQTT)
  timing/          LoopTimer (pw_chrono, non-blocking)
        ↓
Pigweed abstractions (pw_log, pw_unit_test, pw_chrono, pw_span, pw_string)
        ↓
Board HAL / Platform (Arduino ESP32 core 3.3.7 — ESP-IDF v5.5.2)
        ↓
cc_toolchain (Xtensa GCC 15.2 — hermetically downloaded)
```

**Pigweed's role** is software abstractions only.  It does **not** provide the Xtensa toolchain; that is handled by the custom `cc_toolchain` in `esp32/`.

---

## Directory Layout

```
tools/firmware/
  BUILD.bazel              # CPU + board constraint_settings
  boards.bzl               # firmware_board() macro — how to add a new board
  flash.bzl                # flash_firmware() macro — universal flash target
  README.md                # this file
  esp32/
    BUILD.bazel            # ESP32 platform + cc_toolchain + board defines
                           #   + bootloader ELF→BIN genrules
                           #   + partition table CSV→BIN genrule
    cc_toolchain_config.bzl  # Xtensa cc_toolchain_config rule
    flash_config.bzl       # ESP32_DIO_80M / ESP32_QIO_80M flash config structs
    xtensa_toolchain.BUILD # build_file for the Xtensa GCC http_archive
    arduino_core.BUILD     # build_file for @arduino_esp32 (framework sources)
    arduino_libs.BUILD     # build_file for @arduino_esp32_libs (ESP-IDF SDK)
    esptool_wrapper.py     # thin py_binary wrapper around the esptool package
    pubsubclient.BUILD     # build_file for @pubsubclient (MQTT client library)
  flash/
    flash.py               # board-agnostic flash driver (esptool / avrdude / picotool)

tools/bazel/
  esp32.bzl                # esp32_firmware() macro → {name}_lib / _elf / _bin / flash

demo/blink/
  blink.cc                 # Arduino sketch using pw_log
  blink_logic_test.cc      # host-side pw_unit_test (no hardware needed)
  BUILD.bazel
```

---

## Supported Boards

| Board | CPU | Toolchain | Flash tool |
|-------|-----|-----------|------------|
| ELEGOO ESP32 (CP2102) | Xtensa LX6 | `xtensa-esp-elf-gcc` 15.2 | `esptool` |

---

## Building Firmware

```bash
# Build the flashable .bin image for the blink demo
bazel build //demo/blink:blink_bin --config=esp32

# Inspect the ELF (must say "Tensilica Xtensa")
file bazel-bin/demo/blink/blink_elf

# Run host-side unit tests — no hardware needed
bazel test //demo/blink:blink_logic_test
bazel test //firmware/...
```

---

## Flashing

### WSL2 USB setup (one-time)

The CP2102 USB-UART bridge requires USB passthrough from Windows to WSL2:

```powershell
# Windows PowerShell (run as Administrator)
# Install usbipd-win if not already installed:
winget install usbipd

# Run usbipd list to find the CP2102 "USB Serial" entry and note its BUS_ID (e.g. 9-1).
# The BUS_ID reflects the physical USB port — it may change if you replug to a different slot.
usbipd list

# Bind marks the device as shareable with WSL. This is a one-time step per device
# and persists across reboots and replugs (even if the BUS_ID changes).
usbipd bind --busid <BUS_ID>

# Attach forwards the device into the running WSL session.
# Re-run this each time you plug in the device or restart WSL.
usbipd attach --wsl --busid <BUS_ID>
```

```bash
# WSL2 — verify the device appeared
ls /dev/ttyUSB*

# If nothing appears, the cp210x driver may need to be loaded manually
# (WSL2 kernels don't always auto-load it):
sudo modprobe cp210x

# Grant access (pick one):
sudo chmod 666 /dev/ttyUSB0        # temporary
sudo usermod -aG dialout $USER     # permanent (re-login required)
```

### Flash via Bazel

```bash
bazel run //demo/blink:flash -- /dev/ttyUSB0
```

This flashes three segments in one esptool invocation:
- `0x1000` — bootloader (generated from ELF shipped in `@arduino_esp32_libs`)
- `0x8000` — partition table (generated from `max_app_4MB.csv` in `@arduino_esp32`)
- `0x10000` — application binary

All three are generated at build time — nothing is checked in as a binary artifact.

### Monitor serial output

`pw_log_basic` writes through `pw_sys_io_stdio` → `printf` → UART0, which is the same CP2102 bridge used for flashing. No separate connection needed.

```bash
# screen (most common)
screen /dev/ttyUSB0 115200
# Ctrl-A K to exit

# pyserial miniterm (already available — esptool pulled it in)
python3 -m serial.tools.miniterm /dev/ttyUSB0 115200
# Ctrl-] to exit

# raw stream (no interactivity, useful for scripting)
stty -F /dev/ttyUSB0 115200 raw && cat /dev/ttyUSB0
```

Output format from `pw_log_basic`:
```
INF Blink starting (LED pin=2)
INF LED ON
INF LED OFF
```

**Tip:** `setup()` runs immediately after reset, before you can open a terminal. If you want to catch the startup log line, open the monitor first, then press the reset button on the board.

---

## Adding a New Board

Adding a board follows a four-step pattern.  Example: Raspberry Pi Pico W.

### 1. Add CPU constraint (if new CPU family)

`tools/firmware/BUILD.bazel` already has `cpu_armv6m` for Cortex-M0+.  Skip if the CPU is already listed.

### 2. Create the board subdirectory

```
tools/firmware/pico/
  BUILD.bazel        # firmware_board() + cc_toolchain targets
  cc_toolchain_config.bzl
```

```starlark
# tools/firmware/pico/BUILD.bazel
load("//tools/firmware:boards.bzl", "firmware_board")

firmware_board(
    name = "pico_w",
    cpu_constraint = "//tools/firmware:cpu_armv6m",
)
```

> **ARM shortcut:** For ARM Cortex-M targets you can use Pigweed's pre-built toolchain (`@pigweed//pw_toolchain:arm_gcc_cortex_m0`) instead of writing a custom `cc_toolchain_config.bzl`.

### 3. Add the SDK to MODULE.bazel

```starlark
http_archive(
    name = "pico_sdk",
    build_file = "//tools/firmware/pico:pico_sdk.BUILD",
    …
)
register_toolchains("//tools/firmware/pico:arm_toolchain")
```

### 4. Create a firmware macro

Model it after `tools/bazel/esp32.bzl`.  The macro should emit `{name}_lib`, `{name}_elf`, and `{name}_bin` (or `{name}_uf2` for Pico).

---

## Pigweed Integration

Pigweed is pulled via `git_override` in `MODULE.bazel` (BCR release lags the codebase).

### Key modules used

| Module | What it provides | ESP32 backend | Host backend |
|--------|-----------------|---------------|--------------|
| `@pigweed//pw_unit_test` | Embedded test framework (googletest-compatible API) | `simple_printing_main` | `googletest_style_event_handler` |
| `@pigweed//pw_log` | Logging interface | `pw_log_basic` (UART) | `pw_log_sys_io` |
| `@pigweed//pw_assert` | Assertion macros | `pw_assert_basic` | `pw_assert_tokenized` |
| `@pigweed//pw_chrono` | System clock / timers (non-blocking) | FreeRTOS tick backend | `std::chrono::steady_clock` |
| `@pigweed//pw_span` | `std::span` for C++17 | (header-only) | (header-only) |
| `@pigweed//pw_string` | Zero-allocation string formatting (`pw::StringBuffer`) | (header-only) | (header-only) |

### Backend selection

Backends are set in `.bazelrc` under `build:esp32`:

```
build:esp32 --@pigweed//pw_log:backend=@pigweed//pw_log_basic
build:esp32 --@pigweed//pw_sys_io:backend=@pigweed//pw_sys_io_stdio
```

`pw_log_basic` writes to `pw_sys_io`, and `pw_sys_io_stdio` maps to `printf`, which on ESP32 goes to UART0 — the same port that `Serial.begin()` configures.

---

## Toolchain Details (ESP32)

### Xtensa GCC 15.2 (Espressif crosstool-NG)

Downloaded hermetically via `http_archive` in `MODULE.bazel`. SHA256 is pinned.

Binary prefix: `xtensa-esp32-elf-` (little-endian; required for ESP32 LX6)

`_GCC_VERSION` in `tools/firmware/esp32/BUILD.bazel` must match the version in the archive URL. A comment in `MODULE.bazel` documents this sync requirement.

### Toolchain binaries

The toolchain uses direct GCC binary labels (`bin/xtensa-esp32-elf-gcc` etc.) — no wrapper scripts. The `cc_toolchain_config.bzl` rule handles flag filtering directly.

### Arduino ESP32 core 3.3.7 (ESP-IDF v5.5.2)

Two archives are used:

| Archive | Bazel name | Contents |
|---------|-----------|----------|
| `esp32-core-3.3.7.zip` | `@arduino_esp32` | Framework C/C++ sources, variant headers, partition CSVs, `gen_esp32part.py` |
| `esp32-libs-3.3.7.zip` | `@arduino_esp32_libs` | Precompiled ESP-IDF SDK (296 include paths, `.a` libs, linker scripts, bootloader ELFs) |

Both are pinned by SHA256 in `MODULE.bazel`.

`arduino_core.BUILD` exposes:
- `@arduino_esp32//:core_c_lib` — Arduino C sources
- `@arduino_esp32//:core_lib` — Arduino C++ sources
- `@arduino_esp32//:main_cpp` — `main.cpp` entry point (**filegroup** — see below)

`arduino_libs.BUILD` exposes:
- `@arduino_esp32_libs//:sdk_lib` — all ESP-IDF precompiled libs + 296-entry include path
- `@arduino_esp32_libs//:bootloader_elf_dio_80m` / `bootloader_elf_qio_80m` — bootloader ELFs

### Partition table

`max_app_4MB.bin` is generated at build time by `//tools/firmware/esp32:max_app_4MB_bin`:

```
@arduino_esp32//:tools/partitions/max_app_4MB.csv
    ↓  gen_esp32part.py (from @arduino_esp32)
bazel-bin/tools/firmware/esp32/max_app_4MB.bin
```

Layout: `nvs@0x9000` (20K), `otadata@0xe000` (8K), `app(factory)@0x10000` (3968K), `coredump@0x3f0000` (64K).

The `factory` subtype is required because `esp_ota_get_running_partition()` (called by `app_main()` before `setup()` runs) returns NULL for `ota_0` partitions with blank `otadata`, causing a boot assert. ESP-IDF v5 also requires an MD5 checksum in the table, which `gen_esp32part.py` adds automatically.

### Bootloader

The bootloader is converted from ELF to flashable binary at build time:

```
@arduino_esp32_libs//:bootloader_elf_dio_80m
    ↓  esptool elf2image
bazel-bin/tools/firmware/esp32/bootloader_dio_80m.bin
```

Both DIO and QIO variants are generated; the flash config struct selects which to use.

### Flash config structs

`flash_config.bzl` defines `ESP32_DIO_80M` and `ESP32_QIO_80M`.  Each struct carries:
- Flash mode flags, baud rate, reset sequence
- `pre_segments` — list of `struct(addr, label, output)` for segments flashed before the app
- `esptool` — label of the esptool py_binary

`flash.bzl` derives rlocation paths from `label + output` automatically; no runfile paths are hardcoded in the config structs.

---

## The `main.cpp` Footgun

Arduino's `main.cpp` calls your `setup()` and `loop()` — the inversion is the entire programming model:

```cpp
// cores/esp32/main.cpp (simplified)
int main() {
    initArduino();
    setup();       // ← calls YOUR function
    for (;;) { loop(); }
}
```

**Why it must be a filegroup, not a cc_library:**

`ld` processes static archives with single-pass, demand-driven semantics: an archive member is only pulled in if it satisfies an undefined symbol already seen when that archive is scanned.  If `main.cpp` is compiled into `core_lib.a`, the link order looks like:

```
ld  user_sketch.o  user_lib.a  core_lib.a
```

When `core_lib.a` is reached, `main.o` is pulled (entry point needs `main()`), creating undefined refs to `setup()` and `loop()`.  But `user_lib.a` was already scanned and discarded — `ld` won't go back.  Result: `undefined reference to 'setup()'`.

The fix: expose `main.cpp` as a raw `filegroup` and list it in `cc_binary` `srcs`.  Object files on the link command line are **always** fully included, bypassing the archive heuristic entirely.

This is why `esp32_firmware()` in `tools/bazel/esp32.bzl` wires it as:

```python
cc_binary(
    name = name + "_elf",
    srcs = ["@arduino_esp32//:main_cpp"],   # object file, not archive member
    deps = [":" + name + "_lib", ...],
)
```
