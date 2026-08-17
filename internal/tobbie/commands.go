package tobbie

import (
	"fmt"
	"strings"

	"github.com/go-ble/ble"
)

// Nordic UART Service (NUS) — the same service the micro:bit's
// MakeCode "bluetooth start UART service" block exposes, confirmed
// in both the Android app (goditech.com.tobbie) and the firmware hex
// (UUID base 6e400000-b5a3-f393-e0a9-e50e24dcca9e embedded in the
// image).
const (
	ServiceUUIDString = "6e400001-b5a3-f393-e0a9-e50e24dcca9e" // NUS
	RXCharUUIDString  = "6e400003-b5a3-f393-e0a9-e50e24dcca9e" // phone -> robot (write)
	TXCharUUIDString  = "6e400002-b5a3-f393-e0a9-e50e24dcca9e" // robot -> phone (notify)
)

// NUS wire constants.
var (
	ServiceUUID = mustUUID(ServiceUUIDString)
	RXCharUUID  = mustUUID(RXCharUUIDString)
	TXCharUUID  = mustUUID(TXCharUUIDString)
)

// newline terminates every command; the firmware splits on
// serial.delimiters(Delimiters.NewLine).
const newline = "\n"

// encMove encodes a movement command, e.g. "A4\n".
func encMove(d Direction) []byte {
	return []byte(d.wire() + newline)
}

// encFace encodes a built-in face command, e.g. "C01\n".
func encFace(f Face) []byte {
	return []byte(f.wire() + newline)
}

// encFaceSet encodes a custom face: "C" + five zero-padded decimal
// row values (0..31) + newline, e.g. "C3110120404\n".
func encFaceSet(rows [5]byte) []byte {
	for _, r := range rows {
		if r > 0x1f {
			panic(fmt.Sprintf("tobbie: row value %d out of range (0..31)", r))
		}
	}
	var sb strings.Builder
	sb.WriteByte('C')
	for _, r := range rows {
		fmt.Fprintf(&sb, "%02d", r)
	}
	sb.WriteString(newline)
	return []byte(sb.String())
}

// encText encodes a scrolling text command: "Z" + text + newline.
func encText(text string) []byte {
	return []byte("Z" + text + newline)
}

// encStopSign encodes the "stop" pictogram command.
func encStopSign() []byte {
	return []byte("B00" + newline)
}

func mustUUID(s string) ble.UUID {
	u, err := ble.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
