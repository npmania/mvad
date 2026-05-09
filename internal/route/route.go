// Package route sets the system default route to a given interface.
package route

import (
	"errors"
	"net/netip"
)

var ErrUnsupported = errors.New("route: unsupported platform")

func Set(iface string, endpoint netip.Addr) error   { return set(iface, endpoint) }
func Unset(iface string, endpoint netip.Addr) error { return unset(iface, endpoint) }
