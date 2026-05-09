// Package status reports WireGuard interface state.
// Read returns a partial snapshot when wgctrl access is denied.
package status

import (
	"errors"
	"fmt"
	"net/netip"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type Snapshot struct {
	Iface         string
	Up            bool
	OperState     string
	PeerKey       wgtypes.Key
	PeerEndpoint  netip.AddrPort
	Relay         string
	RxBytes       int64
	TxBytes       int64
	LastHandshake time.Time
}

var (
	ErrNotConnected = errors.New("status: interface not present")
	ErrUnsupported  = errors.New("status: unsupported platform")
)

func Read(iface string) (Snapshot, error) { return read(iface) }

func Plain(s Snapshot) string {
	if !s.Up {
		return "disconnected\n"
	}
	name := s.Relay
	if name == "" {
		name = s.PeerEndpoint.String()
	}
	if s.LastHandshake.IsZero() {
		return fmt.Sprintf("connected to %s, no handshake yet\n", name)
	}
	return fmt.Sprintf("connected to %s, last handshake %s ago\n", name, humanDuration(time.Since(s.LastHandshake)))
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%dh", int(d/time.Hour))
}
