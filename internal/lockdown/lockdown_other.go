//go:build !linux

package lockdown

import "net/netip"

func on(relayIPs []netip.Addr) error      { return ErrUnsupported }
func off() error                          { return ErrUnsupported }
func refresh(relayIPs []netip.Addr) error { return ErrUnsupported }
