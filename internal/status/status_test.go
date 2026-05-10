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

func TestJSONConnected(t *testing.T) {
	hs := time.Date(2026, 5, 10, 1, 23, 45, 0, time.UTC)
	s := Snapshot{
		Iface:         "mvad-wg0",
		Up:            true,
		OperState:     "up",
		PeerEndpoint:  netip.MustParseAddrPort("1.2.3.4:51820"),
		Relay:         "se-mma-wg-001",
		Entry:         "se-arn-wg-001",
		RxBytes:       12345,
		TxBytes:       6789,
		LastHandshake: hs,
	}
	got, err := JSON(s)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	want := `{"connected":true,"relay":"se-mma-wg-001","entry":"se-arn-wg-001","endpoint":"1.2.3.4:51820","operstate":"up","iface":"mvad-wg0","rx_bytes":12345,"tx_bytes":6789,"last_handshake":"2026-05-10T01:23:45Z"}` + "\n"
	if got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

func TestJSONDisconnected(t *testing.T) {
	got, err := JSON(Snapshot{OperState: "down"})
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	want := `{"connected":false,"operstate":"down"}` + "\n"
	if got != want {
		t.Errorf("JSON = %s, want %s", got, want)
	}
}

func TestWaybarConnected(t *testing.T) {
	s := Snapshot{
		Up:      true,
		Relay:   "se-mma-wg-001",
		RxBytes: 4_500_000_000,
		TxBytes: 12_300_000_000,
	}
	got, err := Waybar(s)
	if err != nil {
		t.Fatalf("Waybar: %v", err)
	}
	want := `{"text":"se-mma-wg-001","alt":"connected","tooltip":"connected to se-mma-wg-001\n12.3 GB ↑ / 4.5 GB ↓","class":"connected","percentage":0}` + "\n"
	if got != want {
		t.Errorf("Waybar = %s, want %s", got, want)
	}
}

func TestWaybarDisconnected(t *testing.T) {
	got, err := Waybar(Snapshot{})
	if err != nil {
		t.Fatalf("Waybar: %v", err)
	}
	want := `{"text":"off","alt":"disconnected","tooltip":"mvad disconnected","class":"disconnected"}` + "\n"
	if got != want {
		t.Errorf("Waybar = %s, want %s", got, want)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1000, "1.0 kB"},
		{12_300_000_000, "12.3 GB"},
		{4_500_000_000, "4.5 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
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
