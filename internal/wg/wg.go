// Package wg manages a WireGuard interface with a single peer.
package wg

import (
	"errors"
	"net/netip"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var ErrUnsupported = errors.New("wg: unsupported platform")

type Config struct {
	Name       string
	PrivateKey wgtypes.Key
	Address    netip.Prefix
	Address6   netip.Prefix
	PeerKey    wgtypes.Key
	Endpoint   netip.AddrPort
	AllowedIPs []netip.Prefix
	MTU        int
}

type State struct {
	Name          string
	PeerEndpoint  netip.AddrPort
	PeerKey       wgtypes.Key
	RxBytes       int64
	TxBytes       int64
	LastHandshake time.Time
}

func Up(cfg Config) error             { return up(cfg) }
func Down(name string) error          { return down(name) }
func Read(name string) (State, error) { return read(name) }
