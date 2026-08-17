// Package tobbie is a Go client for the Velleman KSR20 "Tobbie II"
// robot. The robot is a BBC micro:bit v2 running MakeCode firmware
// ("12.APP-Remote-Control_Mb-V2") that exposes a Nordic UART (NUS)
// BLE service. The wire protocol was reverse-engineered from the
// official Android app (goditech.com.tobbie) and the MakeCode
// firmware (see README.md).
package tobbie

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Direction is one of the nine movement commands the robot
// understands (wire values "A0".."A8").
type Direction string

const (
	DirForwardLeft   Direction = "forward-left"   // A0
	DirForward       Direction = "forward"        // A1
	DirForwardRight  Direction = "forward-right"  // A2
	DirLeft          Direction = "left"           // A3
	DirStop          Direction = "stop"           // A4
	DirRight         Direction = "right"          // A5
	DirBackwardRight Direction = "backward-right" // A6
	DirBackward      Direction = "backward"       // A7
	DirBackwardLeft  Direction = "backward-left"  // A8
)

// AllDirections lists every valid Direction.
var AllDirections = []Direction{
	DirForwardLeft, DirForward, DirForwardRight,
	DirLeft, DirStop, DirRight,
	DirBackwardRight, DirBackward, DirBackwardLeft,
}

// ParseDirection parses a CLI-style direction name (also accepts the
// raw wire codes "A0".."A8").
func ParseDirection(s string) (Direction, error) {
	norm := strings.ToLower(strings.TrimSpace(s))
	switch norm {
	case "a0":
		return DirForwardLeft, nil
	case "a1":
		return DirForward, nil
	case "a2":
		return DirForwardRight, nil
	case "a3":
		return DirLeft, nil
	case "a4", "halt":
		return DirStop, nil
	case "a5":
		return DirRight, nil
	case "a6":
		return DirBackwardRight, nil
	case "a7":
		return DirBackward, nil
	case "a8":
		return DirBackwardLeft, nil
	}
	for _, d := range AllDirections {
		if string(d) == norm {
			return d, nil
		}
	}
	return "", fmt.Errorf("unknown direction %q (want one of %s)", s, strings.Join(directionNames(), ", "))
}

func directionNames() []string {
	names := make([]string, len(AllDirections))
	for i, d := range AllDirections {
		names[i] = string(d)
	}
	return names
}

// wire returns the two-character command for the direction.
func (d Direction) wire() string {
	return string([]byte{'A', byte(int('0') + directionIndex(d))})
}

func directionIndex(d Direction) int {
	for i, c := range AllDirections {
		if c == d {
			return i
		}
	}
	panic("tobbie: unhandled direction " + string(d))
}

// Face is one of the nine built-in LED faces ("C01".."C09").
type Face int

const (
	Face1 Face = 1 + iota
	Face2
	Face3
	Face4
	Face5
	Face6
	Face7
	Face8
	Face9
)

// MaxFace is the highest built-in face number.
const MaxFace = Face(9)

// ParseFace parses a face number in [1,9].
func ParseFace(s string) (Face, error) {
	var f Face
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &f); err != nil {
		return 0, fmt.Errorf("invalid face %q: must be a number 1..9", s)
	}
	if f < Face1 || f > MaxFace {
		return 0, fmt.Errorf("invalid face %d: must be in 1..9", f)
	}
	return f, nil
}

// wire returns the three-character command for the face.
func (f Face) wire() string {
	return fmt.Sprintf("C%02d", int(f))
}

// ParseFaceGrid parses a custom-face description into five row
// bitmasks (0..31). Two formats are accepted:
//
//   - grid: five rows of five characters ('#' on, '.' off), rows
//     separated by '|', '/' or a newline. Bit 0 is the leftmost LED.
//   - rows: five decimal numbers 0..31, whitespace-separated.
//
// Examples:
//
//	ParseFaceGrid("#...#|#...#|#####|#...#|#...#")  // letter "O"
//	ParseFaceGrid("17 17 31 17 17")                 // same rows
func ParseFaceGrid(s string) (rows [5]byte, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return rows, fmt.Errorf("empty face grid")
	}

	// Rows as decimal numbers.
	if isNumericGrid(s) {
		fields := strings.Fields(s)
		if len(fields) != 5 {
			return rows, fmt.Errorf("expected 5 row values, got %d", len(fields))
		}
		for i, f := range fields {
			var v int
			if _, e := fmt.Sscanf(f, "%d", &v); e != nil {
				return rows, fmt.Errorf("invalid row value %q", f)
			}
			if v < 0 || v > 0x1f {
				return rows, fmt.Errorf("row value %d out of range (0..31)", v)
			}
			rows[i] = byte(v)
		}
		return rows, nil
	}

	// Grid of '#' and '.'.
	lines := splitRows(s)
	if len(lines) != 5 {
		return rows, fmt.Errorf("expected 5 rows, got %d", len(lines))
	}
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) != 5 {
			return rows, fmt.Errorf("row %d has %d cells, want 5", i+1, len(line))
		}
		var v byte
		for j, c := range line {
			switch c {
			case '#':
				v |= 1 << j
			case '.', ' ', '_', 'o':
				// off
			default:
				return rows, fmt.Errorf("row %d: invalid cell %q (use '#' or '.')", i+1, c)
			}
		}
		rows[i] = v
	}
	return rows, nil
}

func isNumericGrid(s string) bool {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		for _, c := range f {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func splitRows(s string) []string {
	for _, sep := range []string{"|", "/"} {
		if strings.Contains(s, sep) {
			parts := strings.Split(s, sep)
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				out = append(out, strings.TrimSpace(p))
			}
			return out
		}
	}
	if strings.Contains(s, "\n") {
		return strings.Split(s, "\n")
	}
	return []string{s}
}

// StepDuration is the approximate duration of one discrete "step" of
// the walk (or one step of turning in place). The firmware has no
// notion of steps — movement commands are latched until a stop
// arrives — so a step is simulated client-side by holding the move
// command for this long and then stopping. Tune it to the robot's
// gait; the TOBBIE_STEP_MS environment variable overrides it.
//
// Calibrated on the robot: requesting 5 steps produced only 2 physical
// steps, i.e. one physical step takes ~625ms of gait.
var StepDuration = 625 * time.Millisecond

// TurnCalibration180 is the measured duration of a 180° turn in place
// (left or right), calibrated on the robot. Roughly proportional: a
// 90° turn is about 750ms, so duration_ms for an angle a is
// a/180 * TurnCalibration180. Surfaces vary; recalibrate by watching
// the robot turn and adjusting duration_ms.
var TurnCalibration180 = 1500 * time.Millisecond

// ApplyStepDurationEnv overrides StepDuration from the TOBBIE_STEP_MS
// environment variable (milliseconds) when set. Call it at startup in
// the CLI and MCP server.
func ApplyStepDurationEnv() {
	if v := os.Getenv("TOBBIE_STEP_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			StepDuration = time.Duration(ms) * time.Millisecond
		}
	}
}

// pulse holds direction d for dur and then stops the robot. It
// aborts early on ctx cancellation but always sends the stop command
// so the robot never keeps walking after a cancel.
func pulse(ctx context.Context, r Robot, d Direction, dur time.Duration) error {
	if err := r.Move(d); err != nil {
		return err
	}
	select {
	case <-time.After(dur):
	case <-ctx.Done():
	}
	return r.Move(DirStop)
}

// Robot is the interface to all main Tobbie II functionalities over
// BLE. Implementations: *BLE (real hardware via go-ble) and *Mock
// (in-memory, for tests and --sim).
type Robot interface {
	io.Closer

	// Connect pairs with the micro:bit at the given BLE address and
	// discovers the Nordic UART service.
	Connect(ctx context.Context, addr string) error

	// Disconnect closes the BLE link but keeps the robot usable.
	Disconnect() error

	// Connected reports whether a BLE link is currently open.
	Connected() bool

	// Move issues one of the nine movement commands (A0..A8).
	//
	// The firmware treats movement as latched: the robot keeps
	// walking/turning until a stop command (DirStop) arrives. Use
	// Step or MoveFor for discrete, self-stopping movement.
	Move(d Direction) error

	// Step moves in direction d for n discrete steps and then stops.
	// Each step lasts StepDuration. n <= 0 just sends the stop
	// command. Returns early if ctx is canceled, still stopping the
	// robot.
	Step(ctx context.Context, d Direction, n int) error

	// MoveFor holds direction d for dur and then stops. Use it for
	// fine-grained movement such as a short turn ("a little left").
	// Returns early on ctx cancellation but always stops the robot.
	MoveFor(ctx context.Context, d Direction, dur time.Duration) error

	// SetFace shows one of the nine built-in faces (C01..C09).
	SetFace(f Face) error

	// DrawFace draws a custom face. Each of the five rows is a
	// bitmask of the five LEDs: bit 0 is the leftmost LED. The
	// wire command is "C" followed by five zero-padded decimal
	// row values (e.g. "C3110120404...", 11 chars total), which
	// the firmware renders with TobbieII.drawface.
	DrawFace(rows [5]byte) error

	// ShowText scrolls a message across the LED matrix. Wire
	// command: "Z" + text.
	ShowText(text string) error

	// ShowStopSign renders the "stop" pictogram on the LED matrix
	// (wire command "B00").
	ShowStopSign() error
}

// ErrNotConnected is returned by commands when no BLE link is open.
var ErrNotConnected = errors.New("tobbie: not connected")
