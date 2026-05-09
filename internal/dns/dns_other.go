//go:build !linux

package dns

import "net/netip"

func set([]netip.Addr) error { return ErrUnsupported }
func restore() error         { return ErrUnsupported }
