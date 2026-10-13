//go:build !linux && !darwin

package dns

import "net/netip"

func set(string, []netip.Addr) error { return ErrUnsupported }
func restore(string) error           { return ErrUnsupported }
