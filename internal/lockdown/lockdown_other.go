//go:build !linux

package lockdown

import "net/netip"

func on([]netip.Addr, string) error      { return ErrUnsupported }
func off() error                         { return ErrUnsupported }
func refresh([]netip.Addr, string) error { return ErrUnsupported }
func active() bool                       { return false }
