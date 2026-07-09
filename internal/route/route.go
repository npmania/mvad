// Package route sets the system default route to a given interface.
package route

import (
	"errors"
	"net/netip"
)

var ErrUnsupported = errors.New("route: unsupported platform")

func Set(iface string, endpoint netip.Addr) error   { return set(iface, endpoint) }
func Unset(iface string, endpoint netip.Addr) error { return unset(iface, endpoint) }

// SetLAN routes multicast out the LAN device instead of the tunnel, so
// service discovery (mDNS, SSDP) still reaches the local network. dev is
// the pre-tunnel default interface.
func SetLAN(dev string) error { return setLAN(dev) }
func UnsetLAN() error         { return unsetLAN() }

// Default returns the gateway and device of the IPv4 default route.
func Default() (netip.Addr, string, error) { return defaultRoute() }

// Default6 returns the gateway and device of the IPv6 default route.
func Default6() (netip.Addr, string, error) { return defaultRoute6() }
