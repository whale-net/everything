# Adding a Board

Firmware code never names a board. It includes `board_pins.h` and uses
logical pin names (`board::kLed`, `board::kSda`). The board is picked at build
time by a Bazel platform, which selects the pin header, the toolchain, and the
Arduino board defines. The flash parameters come from a config struct passed
to the firmware macro.

Today there is one board, the ELEGOO ESP32 dev board
(`//tools/firmware/esp32:elegoo_esp32`), selected by `--config=esp32`. This doc
covers adding another ESP32 board, which is a few lines of Starlark, and what
a new CPU family would need, which is a real porting job.

For the toolchain itself, flashing from WSL2, and the Arduino core setup, see
[tools/firmware/README.md](../tools/firmware/README.md).

## How a board is wired together

| Piece | Where | What it does |
|-------|-------|--------------|
| `constraint_value(board_<name>)` | `tools/firmware/BUILD.bazel` | Board identity |
| `constraint_value(cpu_<family>)` | `tools/firmware/BUILD.bazel` | CPU family. `cpu_xtensa`, `cpu_armv6m` (RP2040), and `cpu_armv7em` (STM32) exist; only Xtensa has a toolchain |
| `firmware_board()` | `tools/firmware/boards.bzl` | Declares the `platform` from the OS, CPU, and board constraints |
| `config_setting(is_<name>)` | the board's package | Lets `select()` branch on the board |
| `board_pins()` | `tools/firmware/pins.bzl` | Generates `board_pins.h` plus a `cc_library` for one board |
| `board_pins_registry()` | `tools/firmware/BUILD.bazel` | Builds the `//tools/firmware:board_pins` alias that `select()`s the right pins. Host builds get the empty `:no_board_pins` |
| `arduino_board_defines` | `tools/firmware/esp32/BUILD.bazel` | `ARDUINO_BOARD`/`ARDUINO_VARIANT` defines for the Arduino core, via `select()` |
| Flash config struct | `tools/firmware/<family>/flash_config.bzl` | Flash tool, chip, baud, flash mode, segment offsets |
| `esp32_firmware()` | `tools/bazel/esp32.bzl` | Generates `{name}_lib`, `{name}_elf`, `{name}_bin`, `{name}_merged_bin`, and `:flash` |
| `flash_firmware()` | `tools/firmware/flash.bzl` | Generates the `:flash` wrapper script around `tools/firmware/flash/flash.py` |
| `build:<config>` lines | `.bazelrc` | Sets `--platforms` and the Pigweed backends |

## Adding an ESP32 board

Same Xtensa toolchain, same Arduino core. You add a board identity, a
platform, and a pin map. The example below adds a HiLetgo NodeMCU-32S.

1. Register the board identity in `tools/firmware/BUILD.bazel`:

   ```starlark
   constraint_value(
       name = "board_hiletgo_esp32",
       constraint_setting = ":board",
   )
   ```

2. Declare the platform, config setting, and pins in
   `tools/firmware/esp32/BUILD.bazel`, next to the ELEGOO entries. Pass
   `board_constraint` and the FreeRTOS constraint. Without `board_constraint`
   the pins `select()` falls through to `no_board_pins` and the compile fails
   on a missing `board_pins.h`.

   ```starlark
   firmware_board(
       name = "hiletgo_esp32",
       cpu_constraint = "//tools/firmware:cpu_xtensa",
       board_constraint = "//tools/firmware:board_hiletgo_esp32",
       extra_constraints = ["@pigweed//pw_build/constraints/rtos:freertos"],
   )

   config_setting(
       name = "is_hiletgo_esp32",
       constraint_values = ["//tools/firmware:board_hiletgo_esp32"],
   )

   board_pins(
       name = "hiletgo_esp32_pins",
       board_name = "hiletgo_esp32",
       pins = {
           "Led": 2,
           "Sda": 21,
           "Scl": 22,
       },
   )
   ```

   Several `board_pins()` calls can share a package. Each writes its header
   to its own `<name>/board_pins.h` subdirectory.

3. Add the board to `board_pins_registry()` in `tools/firmware/BUILD.bazel`:

   ```starlark
   board_pins_registry(
       name = "board_pins",
       boards = {
           "//tools/firmware/esp32:is_elegoo_esp32": "//tools/firmware/esp32:elegoo_esp32_pins",
           "//tools/firmware/esp32:is_hiletgo_esp32": "//tools/firmware/esp32:hiletgo_esp32_pins",
       },
   )
   ```

4. If the board isn't a generic `ESP32_DEV`, add an `":is_hiletgo_esp32"`
   branch to `arduino_board_defines` in `tools/firmware/esp32/BUILD.bazel`.
   The default branch already covers DevKit-style boards.

5. Add a config to `.bazelrc`. Copy the `build:esp32` block and change the
   platform:

   ```
   build:hiletgo_esp32 --platforms=//tools/firmware/esp32:hiletgo_esp32
   build:hiletgo_esp32 --@pigweed//pw_log:backend=@pigweed//pw_log_basic
   build:hiletgo_esp32 --@pigweed//pw_sys_io:backend=@pigweed//pw_sys_io_stdio
   build:hiletgo_esp32 --@rules_python//python/config_settings:python_version=3.13
   ```

6. Pick a flash config. `esp32_firmware()` defaults to `ESP32_DIO_80M` (DIO,
   80 MHz, 4 MB), which suits most DevKit clones. For modules with full
   quad-IO, pass `ESP32_QIO_80M`:

   ```starlark
   load("//tools/firmware/esp32:flash_config.bzl", "ESP32_QIO_80M")

   esp32_firmware(
       name = "blink",
       srcs = ["blink.cc"],
       deps = ["@pigweed//pw_log"],
       flash_config = ESP32_QIO_80M,
   )
   ```

   For a different flash size or partition table, add a new struct to
   `tools/firmware/esp32/flash_config.bzl` itself. Its `_seg()` helper is
   private to that file. Each `pre_segments` entry points at a genrule (a
   bootloader `.bin` or partition table) that must exist in
   `tools/firmware/esp32/BUILD.bazel`.

Check it:

```bash
bazel build //demo/blink:blink_bin --config=hiletgo_esp32
bazel run //demo/blink:flash --config=hiletgo_esp32 -- /dev/ttyUSB0
```

`:flash` depends on the firmware image, so it needs the board's `--config`
too. Without one, Bazel fails with "target platform didn't satisfy
constraints".

To confirm the right pins were selected, look at the generated header:
`bazel-bin/tools/firmware/esp32/hiletgo_esp32_pins/board_pins.h`.

## Pins

`tools/firmware/pins.bzl` defines the schema:

- `REQUIRED_PINS`: `Led`. `board_pins()` fails at analysis time if it's
  missing. Use `-1` if the board has no LED.
- `OPTIONAL_PINS`: `Sda`, `Scl`, `Rx2`, `Tx2`, `Adc0`. An omitted pin is set
  to `-1` and gets `BOARD_PIN_<NAME>_SUPPORTED 0`.
- Any other name you put in `pins` is passed through as-is, with no
  `_SUPPORTED` flag.

The generated header has C macros (`BOARD_PIN_LED`, `BOARD_PIN_SDA`,
`BOARD_PIN_SDA_SUPPORTED`) and C++ constants (`board::kLed`, `board::kSda`).
Guard optional pins:

```cpp
#include "board_pins.h"  // dep: //tools/firmware:board_pins

pinMode(board::kLed, OUTPUT);
#if BOARD_PIN_SDA_SUPPORTED
Wire.begin(board::kSda, board::kScl);
#endif
```

To add a new logical pin, add its name to `OPTIONAL_PINS`, then set it in the
`board_pins()` call of each board that has it. Boards that don't have it need
no change.

## Adding a new CPU family

None of this exists yet for any non-Xtensa CPU. The CPU constraint values are
there, but nothing else is. Plan on writing:

1. **Toolchain.** Add the cross-compiler as an `http_archive` in
   `MODULE.bazel` with a build file modelled on
   `tools/firmware/esp32/xtensa_toolchain.BUILD`. Instantiate the generic
   `cc_toolchain_config` rule from `tools/firmware/cc_toolchain_config.bzl`
   (its docstring walks through the arguments), wrap it in `cc_toolchain` and
   `toolchain()`, and add a `register_toolchains()` call to `MODULE.bazel`.
2. **SDK.** Add the vendor SDK as an `http_archive` with a `BUILD` file that
   wraps it as `cc_library` targets, following
   `tools/firmware/esp32/arduino_core.BUILD`. If the SDK provides `main()`,
   expose that file as a `filegroup` and put it in the binary's `srcs`, not
   in a `cc_library`, or the linker drops it. The "`main.cpp` Footgun"
   section of `tools/firmware/README.md` explains why.
3. **Board.** Constraint, `firmware_board()`, `config_setting`, `board_pins()`,
   and a registry entry, as in the ESP32 steps above, in a new
   `tools/firmware/<family>/` package.
4. **Flash config.** A struct in `tools/firmware/<family>/flash_config.bzl`.
   `flash_firmware()` generates wrappers for `esptool`, `avrdude`, and
   `picotool`. `flash.py` also has an `openocd` path, but `flash.bzl` doesn't
   emit it yet. A new tool needs a function and a `dispatch` entry in
   `tools/firmware/flash/flash.py` and a matching branch in
   `flash_firmware()`.
5. **Firmware macro.** A `tools/bazel/<family>.bzl` with a
   `<family>_firmware()` macro modelled on `esp32_firmware()`. It must
   produce a `{name}_bin` target whose output is `{name}.bin`, because
   `flash_firmware()` looks for that file.
6. **`.bazelrc` config** for the board's platform and Pigweed backends.
