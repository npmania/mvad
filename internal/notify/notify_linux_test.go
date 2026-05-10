//go:build linux

package notify

import "testing"

func TestSendMissingBin(t *testing.T) {
	t.Setenv("PATH", "/dev/null")
	if err := Send("title", "body"); err != nil {
		t.Fatal(err)
	}
}

func TestSendNoSessionBus(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	if err := Send("title", "body"); err != nil {
		t.Fatal(err)
	}
}
