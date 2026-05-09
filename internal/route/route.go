// Package route sets the system default route to a given interface.
package route

import "errors"

var ErrUnsupported = errors.New("route: unsupported platform")

func Set(iface string) error   { return set(iface) }
func Unset(iface string) error { return unset(iface) }
