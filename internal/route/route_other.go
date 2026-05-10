//go:build !linux

package route

import "net/netip"

func set(string, netip.Addr) error              { return ErrUnsupported }
func unset(string, netip.Addr) error            { return ErrUnsupported }
func defaultRoute() (netip.Addr, string, error) { return netip.Addr{}, "", ErrUnsupported }
