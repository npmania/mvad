//go:build linux

package firewall

import (
	"net/netip"
	"strings"
	"testing"
)

func lanConfig() Config {
	return Config{
		Iface:    "mvad-wg0",
		Endpoint: netip.MustParseAddrPort("185.65.135.100:51820"),
		AllowLAN: true,
	}
}

func TestAllowLANCoversDiscovery(t *testing.T) {
	s := buildScript(lanConfig())
	want := []string{
		"ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 } accept",
		"ip saddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 } accept",
		"ip6 daddr { fe80::/10, ff02::/16 } accept",
		"ip6 saddr { fe80::/10, ff02::/16 } accept",
	}
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("script missing rule:\n\t%s\ngot:\n%s", w, s)
		}
	}
}

func TestAllowLANOffOmitsRules(t *testing.T) {
	c := lanConfig()
	c.AllowLAN = false
	s := buildScript(c)
	if strings.Contains(s, "224.0.0.0/4") || strings.Contains(s, "ff02::/16") {
		t.Errorf("allow-lan off but discovery rules present:\n%s", s)
	}
}
