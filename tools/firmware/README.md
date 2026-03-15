# Firmware Build Infrastructure

Hermetic embedded firmware development inside the Bazel monorepo.
The core idea: **declare a board once, and any firmware that targets it compiles and flashes correctly** — no per-developer toolchain setup required.

---

## Architecture

```
Application Logic (cc_library, board-agnostic)
        ↓
Pigweed Abstractions (pw_log, pw_unit_test, pw_assert, pw_rpc)
        ↓
Board HAL / Platform (ESP32, Pico, STM32, …)
        ↓
cc_toolchain (Xtensa GCC, ARM GCC, …)
```

**Pigweed's role** is software abstractions only — tokenized logging (`pw_log`), an embedded-friendly test framework (`pw_unit_test`), assertions (`pw_assert`), and host↔device RPC (`pw_rpc`).  Pigweed does **not** provide the Xtensa toolchain; that is handled by the custom `cc_toolchain` in `esp32/`.

---

## Directory Layout

```
tools/firmware/
  BUILD.bazel              # CPU + board constraint_settings
  boards.bzl               # firmware_board() macro — how to add a new board
  README.md                # this file
  esp32/
    BUILD.bazel            # ESP32 platform + cc_toolchain + toolchain target
    cc_toolchain_config.bzl  # Xtensa cc_toolchain_config rule
    cc_wrapper.sh          # strips unsupported host flags before invoking GCC
    xtensa_toolchain.BUILD # build_file for the Xtensa GCC http_archive
    arduino_core.BUILD     # build_file for the Arduino ESP32 core http_archive
    esptool_wrapper.py     # thin py_binary wrapper around the esptool package
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

# Inspect the ELF
file bazel-bin/demo/blink/blink_elf
# → ELF 32-bit LSB executable, Tensilica Xtensa, …

# Run host-side unit tests (no hardware needed)
bazel test //demo/blink:blink_logic_test
```

---

## Flashing

### WSL2 USB setup (one-time)

The CP2102 USB-UART bridge requires USB passthrough from Windows to WSL2:

```powershell
# Windows PowerShell (run as Administrator)
usbipd list
usbipd attach --wsl --busid <BUS_ID>
```

```bash
# WSL2 — verify the device appeared
ls /dev/ttyUSB*

# Grant access (pick one):
sudo chmod 666 /dev/ttyUSB0        # temporary
sudo usermod -aG dialout $USER     # permanent (re-login required)
```

### Flash via Bazel

```bash
bazel run //demo/blink:flash -- /dev/ttyUSB0
```

### Monitor serial output

```bash
screen /dev/ttyUSB0 115200
# Ctrl-A K to exit screen
```

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

Pigweed is pulled in via `git_override` in `MODULE.bazel` (the BCR release lags the actual codebase).

### Key modules used

| Module | What it provides | ESP32 backend | Host backend |
|--------|-----------------|---------------|--------------|
| `@pigweed//pw_unit_test` | Embedded test framework (googletest-compatible API) | `simple_printing_main` | `googletest_style_event_handler` |
| `@pigweed//pw_log` | Logging interface | `pw_log_basic` (UART) | `pw_log_sys_io` |
| `@pigweed//pw_assert` | Assertion macros | `pw_assert_basic` | `pw_assert_tokenized` |
| `@pigweed//pw_span` | `std::span` for C++17 | (header-only) | (header-only) |

### Backend selection

The ESP32 pw_log backend is set in `.bazelrc`:

```
build:esp32 --@pigweed//pw_log:backend=@pigweed//pw_log_basic
```

---

## Toolchain Details (ESP32)

### Xtensa GCC 15.2 (Espressif crosstool-NG)

Downloaded hermetically via `http_archive` in `MODULE.bazel`.  After the first download, fill in the `sha256`:

```bash
sha256sum /tmp/xtensa.tar.xz   # paste into MODULE.bazel
```

Binary prefix: `xtensa-esp-elf-`

### cc_wrapper.sh

Required because `.bazelrc` sets `--incompatible_strict_action_env`.  The wrapper strips x86-only flags (`-march=`, `-msse*`, `-fstack-clash-protection`, etc.) before forwarding to `xtensa-esp-elf-gcc`.

### Arduino ESP32 core 1.0.6

Pulled as `@arduino_esp32` via `http_archive`.  The `arduino_core.BUILD` build file exposes:

- `@arduino_esp32//:core_c_lib` — C sources
- `@arduino_esp32//:core_lib` — C++ sources (depends on `core_c_lib`)
- `@arduino_esp32//:main_cpp` — `main.cpp` entry point (add to `cc_binary` srcs directly)
- `@arduino_esp32//:bootloader` — pre-compiled bootloader blobs

**Migration path to 3.x:** Update the `http_archive` URL/sha256, rewrite `arduino_core.BUILD` (response-file flags in 3.x vs inline in 1.0.6), and update C++ standard flags (`gnu17`/`gnu++17` instead of `gnu11`/`gnu++11`).
