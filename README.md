# tobbie2

> Control the **Velleman KSR20 "Tobbie II"** robot (a BBC micro:bit v2
> running the stock MakeCode Bluetooth remote-control firmware) over
> Bluetooth Low Energy — from Go, the CLI, or an AI agent via MCP.

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/mheers/tobbie2.svg)](https://pkg.go.dev/github.com/mheers/tobbie2)
[![Go Report Card](https://goreportcard.com/badge/github.com/mheers/tobbie2)](https://goreportcard.com/report/github.com/mheers/tobbie2)

This repo contains a reverse-engineered BLE client for the Tobbie II
robotics kit: a Go library (`internal/tobbie`), a Cobra CLI (`tobbie`)
and a Model Context Protocol server (`tobbie-mcp`) so AI agents can
drive the robot. No documentation exists from Velleman — the wire
protocol was recovered from the firmware, the Android app and the
device itself (see below).

## Install

Requires Go 1.25+ and Linux with a Bluetooth adapter.

```sh
# CLI
go install github.com/mheers/tobbie2/cmd/tobbie@latest

# MCP server
go install github.com/mheers/tobbie2/cmd/tobbie-mcp@latest
```

## Quick start

```sh
# scan for the micro:bit (advertises e.g. "BBC micro:bit [abcdef]")
sudo tobbie scan --name micro:bit

# connect and remember the address
sudo tobbie connect --mac D3:82:15:8F:29:01

# drive it
tobbie move forward
tobbie move forward-left
tobbie move stop
tobbie face 5
tobbie face-set '#...#|#...#|#####|#...#|#...#'
tobbie text "hello from golang"
tobbie stop-sign
```

`--sim` runs every command against an in-memory mock and prints the
exact bytes that would be sent over the air — no hardware needed:

```sh
tobbie --sim move forward        # mock: move forward -> "A1\n"
tobbie --sim face-set 17 17 31 17 17   # mock: -> "C1717311717\n"
```

## Requirements

- Linux with a Bluetooth adapter.
- The go-ble transport uses raw HCI sockets: run as **root** or grant
  `CAP_NET_ADMIN`/`CAP_NET_RAW` (e.g. `sudo setcap cap_net_raw,cap_net_admin=eip tobbie`).
- The robot's micro:bit v2 must run the Bluetooth remote-control
  firmware (the MakeCode project that produces `sketch.js`, e.g.
  `12.APP-Remote-Control_Mb-V2.hex`).

## The wire protocol (reverse-engineered)

Nothing here is documented by Velleman; it was recovered from three
artifacts that ship with the product (the `.pdf`, `.apk` and `.hex`
are gitignored, see [.gitignore](.gitignore)):

1. **`sketch.js`** — the MakeCode JavaScript (converted from the
   `.hex`) running on the robot. It defines every command the robot
   understands.
2. **`12.APP-Remote-Control_Mb-V2.apk`** (package
   `goditech.com.tobbie`) — the official Android app, decompiled:
   `TsCommand.GetCommand()` maps the app's command codes to the wire
   strings, `BluetoothActivity` shows the GATT service/characteristic
   and write mechanics.
3. **`12.APP-Remote-Control_Mb-V2.hex`** — contains the Nordic UART
   base UUID (`6e400000-b5a3-f393-e0a9-e50e24dcca9e`) embedded in the
   firmware image.

### BLE connection

The robot exposes the standard **Nordic UART Service (NUS)** — the
same service MakeCode's "bluetooth start UART service" block creates:

| Role         | UUID                                   | Usage                        |
|--------------|----------------------------------------|------------------------------|
| Service      | `6e400001-b5a3-f393-e0a9-e50e24dcca9e` | Nordic UART service          |
| RX (write)   | `6e400003-b5a3-f393-e0a9-e50e24dcca9e` | phone → robot: all commands  |
| TX (notify)  | `6e400002-b5a3-f393-e0a9-e50e24dcca9e` | robot → phone (unused)       |

Every command is ASCII text terminated with `\n` (the firmware splits
incoming data on `serial.delimiters(Delimiters.NewLine)`). The app
writes with response on `6e400003`; this client does the same.

### Commands

**Movement — `A0`…`A8`** (two chars, from `sketch.js`):

| Wire | Semantics (from the firmware)          | `tobbie move`       |
|------|----------------------------------------|---------------------|
| `A0` | forward + turn left                    | `forward-left`      |
| `A1` | forward                                | `forward`           |
| `A2` | forward + turn right                   | `forward-right`     |
| `A3` | turn left (in place)                   | `left`              |
| `A4` | stop                                   | `stop`              |
| `A5` | turn right (in place)                  | `right`             |
| `A6` | backward + turn right                  | `backward-right`    |
| `A7` | backward                               | `backward`          |
| `A8` | backward + turn left                   | `backward-left`     |

Movement is **latched**: the firmware keeps walking/turning until a
stop (`A4`) arrives — there is no notion of "one step" or "a few
degrees" on the robot. Discrete movement is therefore simulated
client-side: `tobbie move forward --steps 3` (or MCP `steps`) sends
`A1`, holds it for `n × StepDuration` (default 250ms per step) and
then sends `A4`. Fine-grained turns work the same way with an
explicit duration, e.g. `tobbie move left --duration 300ms` (MCP
`duration_ms`). The step length is tunable via the `TOBBIE_STEP_MS`
environment variable (milliseconds), e.g. `TOBBIE_STEP_MS=150`.

**Turn calibration (measured on the robot):** `duration_ms: 1500` is a
180° turn in place (left or right); 90° is about 750ms. Scale
proportionally for other angles (`angle / 180 × 1500`). This is
exposed as `tobbie.TurnCalibration180` in Go and baked into the CLI
`--duration` and MCP `duration_ms` descriptions. Surfaces vary — if a
turn comes out short or overshoots, adjust the duration and
re-measure.

The APK's `moveDetector` confirms the mapping: joystick direction →
`GetCommand(-9 … -17)` → `A0…A8` in exactly this order.

**Built-in faces — `C01`…`C09`** (three chars). The LED patterns are
hardcoded in the firmware; see `sketch.js`.

**Custom face — `C` + 10 digits.** `C` followed by five zero-padded
decimal values 0..31, one per LED row (each row is a 5-bit mask). The
app builds this in `FaceControlActivity`; the firmware renders it with
`TobbieII.drawface()`. Example: `C1717311717` = an "O" (rows
`10001 10001 11111 10001 10001`, bit 0 = leftmost LED).

**Scrolling text — `Z` + message.** The firmware calls
`basic.showString()` on the payload after the `Z` (`SendCommandText`).

**Stop pictogram — `B00`** (three chars). Supported by the firmware;
the stock app never sends it.

## The Go interface

`internal/tobbie.Robot` is the interface to every main functionality:

```go
type Robot interface {
    io.Closer
    Connect(ctx context.Context, addr string) error
    Disconnect() error
    Connected() bool
    Move(d Direction) error                              // latched: moves until DirStop
    Step(ctx context.Context, d Direction, n int) error  // n discrete steps, then stop
    MoveFor(ctx context.Context, d Direction, dur time.Duration) error // hold then stop
    SetFace(f Face) error
    DrawFace(rows [5]byte) error
    ShowText(text string) error
    ShowStopSign() error
}
```

`Step` and `MoveFor` are client-side simulations of discrete movement
(see above); both always stop the robot, even when their context is
canceled. `StepDuration` (default 250ms, override with
`TOBBIE_STEP_MS`) is the exported, tunable length of one step.

Implementations:

- `tobbie.NewBLE()` — real hardware via
  [`github.com/go-ble/ble`](https://github.com/go-ble/ble) (raw HCI,
  no BlueZ daemon).
- `tobbie.NewMock()` — in-memory robot recording every command's wire
  bytes (`--sim`).

## CLI reference

| Command                         | Purpose                                          |
|---------------------------------|--------------------------------------------------|
| `tobbie scan [--name <sub>]`    | list nearby BLE devices (name, address, RSSI)    |
| `tobbie connect [--mac <addr>]` | connect and save the address in `~/.config/tobbie2/config.json` |
| `tobbie status`                 | show saved address, try a link                  |
| `tobbie move <direction>`       | movement command (also accepts raw `a0`..`a8`); `--steps N` moves N steps (~500ms each) then stops, `--duration 300ms` holds the movement then stops (1500ms ≈ 180° turn), default keeps moving until a stop |
| `tobbie face <1-9>`             | built-in face                                   |
| `tobbie face-set <grid\|rows>`  | custom face (grid of `#`/`.` or five 0..31 rows) |
| `tobbie text <message>`         | scrolling text                                  |
| `tobbie stop-sign`              | stop pictogram                                  |
| `tobbie disconnect`             | close the link (commands close it automatically)|

Global flags: `--mac`, `--sim`, `--timeout`.

## MCP server

`tobbie-mcp` exposes the robot as a Model Context Protocol server
(built on [`github.com/mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go)),
so AI agents can scan, connect and drive the robot as MCP tools. It
speaks stdio JSON-RPC and uses the same saved address as the CLI.

```sh
go build -o tobbie-mcp ./cmd/tobbie-mcp
sudo setcap cap_net_raw,cap_net_admin=eip tobbie-mcp   # same BLE caps as the CLI
tobbie-mcp
```

Tools: `scan`, `connect`, `status`, `move`, `face`, `face-set`,
`text`, `stop-sign`, `disconnect` — one tool per CLI command, each
opening a fresh BLE link like the CLI does — plus `move-natural` when
`TYPESAFE_API_KEY` is set (see below).

The `move` tool accepts optional `steps` (e.g. `steps: 1` for "geh
einen Schritt", `steps: 5` for five) or `duration_ms` (e.g. `300` for
"dreh dich ein bisschen"); both make the robot stop automatically
after the movement. Without them the robot keeps moving until a stop
command. `steps` and `duration_ms` are mutually exclusive.

### Natural-language movement (optional)

With `TYPESAFE_API_KEY` set, the server also registers `move-natural`,
which resolves a request like *"dreh dich ein bisschen nach links"* or
*"geh drei Schritte vorwärts"* into a movement. Five typed
[TypeSafe](https://docs.typesafe.ai) System One judgements classify the
movement, the side and whether the request is bounded, and grade how
far. The resolver in `internal/judge` then applies the calibration and
the caps (8 steps, 3 s) in code and refuses a request whose direction
or side is below the confidence gates instead of guessing. A typical
call takes ~300 ms and ~1200 tokens. The utterance is sent to the
TypeSafe API; `TYPESAFE_MODEL` pins a model version and
`TYPESAFE_BASE_URL` points at another deployment.

The question wording was tuned and measured with `tools/nl-eval`: it
scores 38–39 of 40 on the tuning set and 10 of 13 on an unseen holdout,
with every failure a refusal rather than a wrong movement.

Set `TOBBIE_SIM=1` to run against the in-memory mock; results then
include the exact wire bytes (`mock wire: "ZHi\n"`), and `connect`
becomes a no-op so your real config is left untouched.

Example opencode config:

```json
{
  "mcp": {
    "tobbie": {
      "type": "stdio",
      "command": "/path/to/tobbie-mcp"
    }
  }
}
```

## Project layout

```
cmd/tobbie/main.go      cobra CLI
cmd/tobbie-mcp/main.go  MCP server (mark3labs/mcp-go)
internal/tobbie/
  tobbie.go             Robot interface, Direction, Face, grid parsing
  intent.go             resolved movement plans (MoveIntent)
  commands.go           NUS UUIDs + wire encoding
  ble.go                go-ble transport (incl. random-address dial)
  scan.go               shared BLE scan helper
  mock.go               in-memory robot
  config.go             saved address
  tobbie_test.go        wire encoding + mock tests
internal/judge/
  judge.go              TypeSafe System One client
  move.go               movement question set + resolver
tools/nl-eval/          prompt evaluation harness (go run ./tools/nl-eval)
```

## License

MIT — see [LICENSE](LICENSE).

The Tobbie II / KSR20 robot and its firmware are products of Velleman
Group nv; the artifacts (`*.pdf`, `*.apk`, `*.hex`) that ship with the
hardware are not part of this repository.