//go:build !linux

package split

import "net/netip"

func up(Config) error              { return ErrUnsupported }
func down() error                  { return ErrUnsupported }
func addPID(int) error             { return ErrUnsupported }
func rmPID(int) error              { return ErrUnsupported }
func listPIDs() ([]int, error)     { return nil, ErrUnsupported }
func setNets([]netip.Prefix) error { return ErrUnsupported }
func clear() error                 { return ErrUnsupported }
func available() bool              { return false }
func splitMode() bool              { return false }
