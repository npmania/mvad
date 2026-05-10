//go:build !linux

package split

import "net/netip"

func up(netip.Addr, string) error { return ErrUnsupported }
func down() error                 { return ErrUnsupported }
func addPID(int) error            { return ErrUnsupported }
func rmPID(int) error             { return ErrUnsupported }
func listPIDs() ([]int, error)    { return nil, ErrUnsupported }
func clear() error                { return ErrUnsupported }
func available() bool             { return false }
