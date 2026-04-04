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
Board HAL / Platform (Arduino ESP32 core 1.0.6)
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
  README.md                # this file
  esp32/
    BUILD.bazel            # ESP32 platform + cc_toolchain + toolchain target
    cc_toolchain_config.bzl  # Xtensa cc_toolchain_config rule
    cc_wrapper.sh          # strips unsupported host flags before invoking GCC
    xtensa_toolchain.BUILD # build_file for the Xtensa GCC http_archive
    arduino_core.BUILD     # build_file for the Arduino ESP32 core http_archive
    esptool_wrapper.py     # thin py_binary wrapper around the esptool package

tools/bazel/
  esp32.bzl                # esp32_firmware() macro → {name}_lib / _elf / _bin

demo/blink/
  blink.cc                 # Arduino sketch using pw_log instead of Serial
  blink_logic_test.cc      # host-side pw_unit_test (no hardware needed)
  flash.sh                 # esptool runfiles wrapper for bazel run
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

The ESP32 pw_log backend is set in `.bazelrc`:

```
build:esp32 --@pigweed//pw_log:backend=@pigweed//pw_log_basic
```

---

## Toolchain Details (ESP32)

### Xtensa GCC 15.2 (Espressif crosstool-NG)

Downloaded hermetically via `http_archive` in `MODULE.bazel`.
sha256 is pinned: `3d50f5cd5f173acfd524e07c1cd69bc99585731a415ca2e5bce879997fe602b8`.

Binary prefix: `xtensa-esp-elf-`

### cc_wrapper.sh

Required because `.bazelrc` sets `--incompatible_strict_action_env`.  The wrapper strips x86-only flags (`-march=`, `-msse*`, `-fstack-clash-protection`, etc.) before forwarding to `xtensa-esp-elf-gcc`.

### Arduino ESP32 core 1.0.6

Pulled as `@arduino_esp32` via `http_archive` (sha256 pinned: `982da9aa…`).  The `arduino_core.BUILD` build file exposes:

- `@arduino_esp32//:core_c_lib` — C sources
- `@arduino_esp32//:core_lib` — C++ sources (depends on `core_c_lib`)
- `@arduino_esp32//:main_cpp` — `main.cpp` entry point (**filegroup, not cc_library — see below**)
- `@arduino_esp32//:bootloader` — pre-compiled bootloader blobs

**Migration path to 3.x:** Update the `http_archive` URL/sha256, rewrite `arduino_core.BUILD` (response-file flags in 3.x vs inline in 1.0.6), and update C++ standard flags (`gnu17`/`gnu++17` instead of `gnu11`/`gnu++11`).

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
