// Package status reports WireGuard interface state.
// Read returns a partial snapshot when wgctrl access is denied.
package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type Snapshot struct {
	Iface         string
	Up            bool
	OperState     string
	PeerKey       wgtypes.Key
	PeerEndpoint  netip.AddrPort
	Relay         string
	Entry         string
	RxBytes       int64
	TxBytes       int64
	LastHandshake time.Time
}

var (
	ErrNotConnected = errors.New("status: interface not present")
	ErrUnsupported  = errors.New("status: unsupported platform")
)

func Read(iface string) (Snapshot, error) { return read(iface) }

func Plain(s Snapshot) string {
	if !s.Up {
		return "disconnected\n"
	}
	name := s.Relay
	if name == "" {
		name = s.PeerEndpoint.String()
	}
	via := ""
	if s.Entry != "" {
		via = " via " + s.Entry
	}
	if s.LastHandshake.IsZero() {
		return fmt.Sprintf("connected to %s%s, no handshake yet\n", name, via)
	}
	return fmt.Sprintf("connected to %s%s, last handshake %s ago\n", name, via, humanDuration(time.Since(s.LastHandshake)))
}

type jsonOut struct {
	Connected     bool   `json:"connected"`
	Relay         string `json:"relay,omitempty"`
	Entry         string `json:"entry,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	OperState     string `json:"operstate,omitempty"`
	Iface         string `json:"iface,omitempty"`
	RxBytes       int64  `json:"rx_bytes,omitempty"`
	TxBytes       int64  `json:"tx_bytes,omitempty"`
	LastHandshake string `json:"last_handshake,omitempty"`
}

func JSON(s Snapshot) (string, error) {
	o := jsonOut{
		Connected: s.Up,
		Relay:     s.Relay,
		Entry:     s.Entry,
		OperState: s.OperState,
		Iface:     s.Iface,
		RxBytes:   s.RxBytes,
		TxBytes:   s.TxBytes,
	}
	if s.PeerEndpoint.IsValid() {
		o.Endpoint = s.PeerEndpoint.String()
	}
	if !s.LastHandshake.IsZero() {
		o.LastHandshake = s.LastHandshake.UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(o)
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

type waybarOut struct {
	Text       string `json:"text"`
	Alt        string `json:"alt"`
	Tooltip    string `json:"tooltip"`
	Class      string `json:"class"`
	Percentage int    `json:"percentage"`
}

func Waybar(s Snapshot) (string, error) {
	if !s.Up {
		data, err := json.Marshal(struct {
			Text    string `json:"text"`
			Alt     string `json:"alt"`
			Tooltip string `json:"tooltip"`
			Class   string `json:"class"`
		}{"off", "disconnected", "mvad disconnected", "disconnected"})
		if err != nil {
			return "", err
		}
		return string(data) + "\n", nil
	}
	name := s.Relay
	if name == "" {
		name = s.PeerEndpoint.String()
	}
	tip := "connected to " + name
	if s.Entry != "" {
		tip += " via " + s.Entry
	}
	if s.TxBytes != 0 || s.RxBytes != 0 {
		tip += fmt.Sprintf("\n%s ↑ / %s ↓", humanBytes(s.TxBytes), humanBytes(s.RxBytes))
	}
	o := waybarOut{Text: name, Alt: "connected", Tooltip: tip, Class: "connected"}
	data, err := json.Marshal(o)
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%dh", int(d/time.Hour))
}

func humanBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"kB", "MB", "GB", "TB", "PB"}
	f := float64(n) / 1000
	i := 0
	for f >= 1000 && i < len(units)-1 {
		f /= 1000
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
