//go:build darwin

package status

import (
	"errors"
	"testing"
)

func TestReadNotConnected(t *testing.T) {
	// lo0 always exists but has no WireGuard socket.
	for _, iface := range []string{"utun32767", "lo0"} {
		s, err := Read(iface)
		if !errors.Is(err, ErrNotConnected) || s.Up {
			t.Errorf("Read(%q) = %+v, %v; want ErrNotConnected", iface, s, err)
		}
	}
}
