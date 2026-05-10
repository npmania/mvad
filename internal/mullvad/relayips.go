package mullvad

import (
	"encoding/json"
	"net/netip"
)

func RelayIPs(raw json.RawMessage) ([]netip.Addr, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rs []Relay
	if err := json.Unmarshal(raw, &rs); err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(rs)*2)
	for _, r := range rs {
		if r.IPv4.IsValid() {
			out = append(out, r.IPv4)
		}
		if r.IPv6.IsValid() {
			out = append(out, r.IPv6)
		}
	}
	return out, nil
}
