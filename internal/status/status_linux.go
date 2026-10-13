//go:build linux

package status

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func read(iface string) (Snapshot, error) {
	state, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "operstate"))
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, ErrNotConnected
	}
	if err != nil {
		return Snapshot{}, err
	}
	oper := strings.TrimSpace(string(state))
	s := Snapshot{
		Iface:     iface,
		OperState: oper,
		Up:        oper == "up" || oper == "unknown",
	}
	return peer(s)
}
