package status

import (
	"net/netip"
	"testing"
	"time"
)

func TestPlainDisconnected(t *testing.T) {
	got := Plain(Snapshot{OperState: "down"})
	want := "disconnected\n"
	if got != want {
		t.Errorf("Plain = %q, want %q", got, want)
	}
}

func TestPlainNoHandshake(t *testing.T) {
	got := Plain(Snapshot{Up: true, Relay: "se-mma-wg-001"})
	want := "connected to se-mma-wg-001, no handshake yet\n"
	if got != want {
		t.Errorf("Plain = %q, want %q", got, want)
	}
}

func TestPlainConnected(t *testing.T) {
	s := Snapshot{
		Up:            true,
		Relay:         "se-mma-wg-001",
		LastHandshake: time.Now().Add(-14 * time.Second),
	}
	got := Plain(s)
	want := "connected to se-mma-wg-001, last handshake 14s ago\n"
	if got != want {
		t.Errorf("Plain = %q, want %q", got, want)
	}
}

func TestPlainMultihop(t *testing.T) {
	s := Snapshot{
		Up:            true,
		Relay:         "us-nyc-wg-001",
		Entry:         "se-mma-wg-002",
		LastHandshake: time.Now().Add(-3 * time.Second),
	}
	got := Plain(s)
	want := "connected to us-nyc-wg-001 via se-mma-wg-002, last handshake 3s ago\n"
	if got != want {
		t.Errorf("Plain = %q, want %q", got, want)
	}
}

func TestPlainEmptyRelayFallback(t *testing.T) {
	s := Snapshot{
		Up:           true,
		PeerEndpoint: netip.MustParseAddrPort("1.2.3.4:51820"),
	}
	got := Plain(s)
	want := "connected to 1.2.3.4:51820, no handshake yet\n"
	if got != want {
		t.Errorf("Plain = %q, want %q", got, want)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{14 * time.Second, "14s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m"},
		{5 * time.Minute, "5m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{2 * time.Hour, "2h"},
		{25 * time.Hour, "25h"},
		{-time.Second, "0s"},
	}
	for _, c := range cases {
		if got := humanDuration(c.in); got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
