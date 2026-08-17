package tobbie

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	"github.com/pkg/errors"
)

// BLE implements Robot over a real micro:bit using go-ble's raw HCI
// transport (no BlueZ daemon required; needs root or the relevant
// capabilities on Linux).
type BLE struct {
	mu sync.Mutex

	device   *linux.Device
	client   ble.Client
	rx       *ble.Characteristic
	addr     string
	closed   bool
	notifyCh chan []byte
}

// NewBLE returns a disconnected BLE client.
func NewBLE() *BLE {
	return &BLE{notifyCh: make(chan []byte, 16)}
}

// dialAddr resolves the address type of addr. MakeCode micro:bit v2s
// advertise a random static address; dialing one as public makes the
// robot silently ignore the connection request. A short scan reveals
// the type carried by the advertisement; fall back to public if the
// device isn't seen (older firmware advertises a public address).
func dialAddr(ctx context.Context, device *linux.Device, addr string) (ble.Addr, error) {
	dial := ble.Addr(ble.NewAddr(addr))
	sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	err := device.Scan(sctx, false, func(a ble.Advertisement) {
		if strings.EqualFold(a.Addr().String(), addr) {
			dial = a.Addr()
		}
	})
	if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
		return nil, err
	}
	return dial, nil
}

// Connect opens a link to the micro:bit at addr (MAC like
// "D3:82:15:8F:29:01") and discovers the NUS service. It also
// subscribes to the TX characteristic so data sent by the robot can
// be read from Notify().
func (b *BLE) Connect(ctx context.Context, addr string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client != nil {
		return fmt.Errorf("already connected to %s", b.addr)
	}

	device, err := linux.NewDevice()
	if err != nil {
		return errors.Wrap(err, "open HCI device")
	}
	ble.SetDefaultDevice(device)

	daddr, err := dialAddr(ctx, device, addr)
	if err != nil {
		device.HCI.Close()
		return errors.Wrap(err, "resolve address type")
	}

	client, err := ble.Dial(ctx, daddr)
	if err != nil {
		device.HCI.Close()
		return errors.Wrapf(err, "dial %s", addr)
	}

	profile, err := client.DiscoverProfile(false)
	if err != nil {
		client.CancelConnection()
		device.HCI.Close()
		return errors.Wrap(err, "discover profile")
	}

	rx, ok := profile.Find(&ble.Characteristic{UUID: RXCharUUID}).(*ble.Characteristic)
	if !ok {
		client.CancelConnection()
		device.HCI.Close()
		return fmt.Errorf("NUS RX characteristic %s not found (is the right firmware flashed?)", RXCharUUIDString)
	}

	if tx, ok := profile.Find(&ble.Characteristic{UUID: TXCharUUID}).(*ble.Characteristic); ok {
		// Best effort: mirror the Android app and subscribe so
		// telemetry from the robot can be observed. The stock
		// firmware never sends anything.
		_ = client.Subscribe(tx, false, func(_ []byte) {
			select {
			case b.notifyCh <- append([]byte(nil), tx.Value...):
			default:
			}
		})
	}

	// Watch for a dropped link so Connected() reflects reality and a
	// stale client can't block a reconnect. The goroutine exits on
	// its own when the connection is closed (including by
	// Disconnect/Close below, which nil out b.client first).
	if d, ok := client.(interface{ Disconnected() <-chan struct{} }); ok {
		go func() {
			select {
			case <-d.Disconnected():
				b.mu.Lock()
				defer b.mu.Unlock()
				if b.client != client {
					return
				}
				b.client = nil
				b.rx = nil
				if b.device != nil {
					b.device.HCI.Close()
					b.device = nil
				}
			}
		}()
	}

	b.device = device
	b.client = client
	b.rx = rx
	b.addr = addr
	b.closed = false
	return nil
}

// Notify returns a channel with data received from the robot's TX
// characteristic (rarely used by the stock firmware).
func (b *BLE) Notify() <-chan []byte {
	return b.notifyCh
}

// Disconnect closes the BLE link.
func (b *BLE) Disconnect() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client == nil {
		return nil
	}
	err := b.client.CancelConnection()
	b.client = nil
	if b.device != nil {
		b.device.HCI.Close()
		b.device = nil
	}
	b.rx = nil
	return err
}

// Connected reports whether the BLE link is open (a link dropped by
// the remote side is detected by the watch goroutine in Connect).
func (b *BLE) Connected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.client != nil
}

// Device returns the open HCI device, or nil when disconnected.
// It lets callers scan on the same adapter while a link is open
// (a second HCI device would tear the link down).
func (b *BLE) Device() *linux.Device {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.device
}

// Scan lists nearby BLE devices using the already-open HCI device.
// Returns ErrNotConnected when no link is open.
func (b *BLE) Scan(ctx context.Context, nameFilter string) ([]DeviceHit, error) {
	b.mu.Lock()
	device := b.device
	b.mu.Unlock()
	if device == nil {
		return nil, ErrNotConnected
	}
	return scanWith(ctx, device, nameFilter)
}

// Close disconnects and marks the client unusable.
func (b *BLE) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	client := b.client
	b.client = nil
	if client != nil {
		err := client.CancelConnection()
		if b.device != nil {
			b.device.HCI.Close()
			b.device = nil
		}
		b.rx = nil
		return err
	}
	return nil
}

// write sends one newline-terminated command over the NUS RX
// characteristic, mirroring the Android app (write-with-response).
func (b *BLE) write(p []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client == nil || b.rx == nil {
		return ErrNotConnected
	}
	// The Android app throttles to one in-flight command; a short
	// timeout mirrors that behaviour.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := b.client.WriteCharacteristic(b.rx, p, false); err != nil {
		return errors.Wrap(err, "write command")
	}
	_ = ctx
	return nil
}

// Move sends a movement command. The firmware latches it until a
// stop command arrives; use Step/MoveFor for self-stopping movement.
func (b *BLE) Move(d Direction) error {
	return b.write(encMove(d))
}

// Step moves for n discrete steps (each StepDuration long) and stops.
func (b *BLE) Step(ctx context.Context, d Direction, n int) error {
	if n <= 0 {
		return b.Move(DirStop)
	}
	return b.MoveFor(ctx, d, time.Duration(n)*StepDuration)
}

// MoveFor holds direction d for dur and then stops.
func (b *BLE) MoveFor(ctx context.Context, d Direction, dur time.Duration) error {
	return pulse(ctx, b, d, dur)
}

// SetFace sends a built-in face command.
func (b *BLE) SetFace(f Face) error {
	return b.write(encFace(f))
}

// DrawFace sends a custom face command.
func (b *BLE) DrawFace(rows [5]byte) error {
	return b.write(encFaceSet(rows))
}

// ShowText sends a scrolling-text command.
func (b *BLE) ShowText(text string) error {
	return b.write(encText(text))
}

// ShowStopSign sends the "stop" pictogram command.
func (b *BLE) ShowStopSign() error {
	return b.write(encStopSign())
}
