package tobbie

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
)

// DeviceHit is one advertisement observed while scanning.
type DeviceHit struct {
	Name string
	Addr string
	RSSI int
}

// Scan lists nearby BLE devices, optionally keeping only those whose
// advertised name contains nameFilter (case-insensitive). Results are
// sorted by signal strength, strongest first. It needs raw HCI access
// (root, or the cap_net_raw/cap_net_admin capabilities on Linux).
func Scan(ctx context.Context, nameFilter string) ([]DeviceHit, error) {
	device, err := linux.NewDevice()
	if err != nil {
		return nil, err
	}
	defer device.HCI.Close()
	return scanWith(ctx, device, nameFilter)
}

// scanWith runs a scan on the given (already open) HCI device.
func scanWith(ctx context.Context, device *linux.Device, nameFilter string) ([]DeviceHit, error) {
	var (
		mu   sync.Mutex
		hits []DeviceHit
	)
	err := device.Scan(ctx, false, func(a ble.Advertisement) {
		name := a.LocalName()
		if nameFilter != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(nameFilter)) {
			return
		}
		mu.Lock()
		hits = append(hits, DeviceHit{Name: name, Addr: a.Addr().String(), RSSI: a.RSSI()})
		mu.Unlock()
	})
	if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
		return nil, err
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].RSSI > hits[j].RSSI })
	return hits, nil
}
