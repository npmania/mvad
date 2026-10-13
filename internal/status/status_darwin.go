//go:build darwin

package status

import (
	"errors"
	"net"
	"os"
)

func read(iface string) (Snapshot, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return Snapshot{}, ErrNotConnected
	}
	oper := "down"
	if ifi.Flags&net.FlagUp != 0 {
		oper = "up"
	}
	s, err := peer(Snapshot{Iface: iface, OperState: oper, Up: oper == "up"})
	// A utun without its UAPI socket is not mvad's tunnel: some other
	// program's interface, or ours already told to go.
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, ErrNotConnected
	}
	return s, err
}
