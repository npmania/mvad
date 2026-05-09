// Package dns sets DNS for an interface and restores it on demand.
package dns

import (
	"errors"
	"net/netip"
)

var ErrUnsupported = errors.New("dns: unsupported platform")

func Set(iface string, servers []netip.Addr) error { return set(iface, servers) }
func Restore(iface string) error                   { return restore(iface) }
