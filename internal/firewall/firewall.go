// Package firewall installs an nft table that blocks all traffic
// except WireGuard, the tunnel, and Mullvad DNS, with optional LAN.
package firewall

import (
	"errors"
	"net/netip"
)

var ErrUnsupported = errors.New("firewall: unsupported platform")

type Config struct {
	Iface    string
	Endpoint netip.AddrPort
	DNS      []netip.Addr
	AllowLAN bool
	TCP      bool
}

func Up(c Config) error { return up(c) }
func Down() error       { return down() }
