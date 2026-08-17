package tobbie

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Mock is an in-memory Robot used for tests and the CLI's --sim mode.
// It records every command sent, so the wire encoding can be
// verified without hardware.
type Mock struct {
	mu        sync.Mutex
	addr      string
	connected bool
	closed    bool

	// Commands holds the raw wire bytes of every command sent.
	Commands [][]byte

	// Last holds the decoded last command per category.
	Last struct {
		Direction Direction
		Face      Face
		Rows      [5]byte
		Text      string
		StopSign  bool
	}
}

// NewMock returns a disconnected mock robot.
func NewMock() *Mock { return &Mock{} }

// Connect remembers the address.
func (m *Mock) Connect(_ context.Context, addr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addr = addr
	m.connected = true
	return nil
}

// Disconnect clears the link.
func (m *Mock) Disconnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addr = ""
	m.connected = false
	return nil
}

// Connected reports whether Connect was called.
func (m *Mock) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

// Close disconnects and marks the mock unusable.
func (m *Mock) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.addr = ""
	m.connected = false
	return nil
}

func (m *Mock) send(p []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.connected {
		return ErrNotConnected
	}
	m.Commands = append(m.Commands, append([]byte(nil), p...))
	return nil
}

// Move records a movement command.
func (m *Mock) Move(d Direction) error {
	if err := m.send(encMove(d)); err != nil {
		return err
	}
	m.mu.Lock()
	m.Last.Direction = d
	m.mu.Unlock()
	return nil
}

// Step records a move-then-stop pulse sequence for n steps.
func (m *Mock) Step(ctx context.Context, d Direction, n int) error {
	if n <= 0 {
		return m.Move(DirStop)
	}
	return m.MoveFor(ctx, d, time.Duration(n)*StepDuration)
}

// MoveFor records a move-then-stop pulse sequence.
func (m *Mock) MoveFor(ctx context.Context, d Direction, dur time.Duration) error {
	return pulse(ctx, m, d, dur)
}

// SetFace records a built-in face command.
func (m *Mock) SetFace(f Face) error {
	if err := m.send(encFace(f)); err != nil {
		return err
	}
	m.mu.Lock()
	m.Last.Face = f
	m.mu.Unlock()
	return nil
}

// DrawFace records a custom face command.
func (m *Mock) DrawFace(rows [5]byte) error {
	if err := m.send(encFaceSet(rows)); err != nil {
		return err
	}
	m.mu.Lock()
	m.Last.Rows = rows
	m.mu.Unlock()
	return nil
}

// ShowText records a text command.
func (m *Mock) ShowText(text string) error {
	if err := m.send(encText(text)); err != nil {
		return err
	}
	m.mu.Lock()
	m.Last.Text = text
	m.mu.Unlock()
	return nil
}

// ShowStopSign records the stop pictogram command.
func (m *Mock) ShowStopSign() error {
	if err := m.send(encStopSign()); err != nil {
		return err
	}
	m.mu.Lock()
	m.Last.StopSign = true
	m.mu.Unlock()
	return nil
}

// String renders a compact trace of all sent commands.
func (m *Mock) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sb strings.Builder
	for i, c := range m.Commands {
		fmt.Fprintf(&sb, "[%d] %q\n", i, c)
	}
	return sb.String()
}
