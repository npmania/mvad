// Package dns swaps /etc/resolv.conf to point at given resolvers
// and restores the original on demand.
package dns

import (
	"errors"
	"net/netip"
)

var ErrUnsupported = errors.New("dns: unsupported platform")

func Set(servers []netip.Addr) error { return set(servers) }
func Restore() error                  { return restore() }
