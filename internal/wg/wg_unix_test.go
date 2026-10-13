//go:build linux || darwin

package wg

import (
	"net/netip"
	"testing"
)

func TestUpInvalid(t *testing.T) {
	addr := netip.MustParsePrefix("10.64.0.2/32")
	ep := netip.MustParseAddrPort("192.0.2.1:51820")
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{Address: addr, Endpoint: ep}, "wg: empty interface name"},
		{Config{Name: Iface, Endpoint: ep}, "wg: no address"},
		{Config{Name: Iface, Address: addr}, "wg: no endpoint"},
	}
	for _, c := range cases {
		err := Up(c.cfg)
		if err == nil || err.Error() != c.want {
			t.Errorf("Up(%+v) = %v, want %s", c.cfg, err, c.want)
		}
	}
}

func TestPrefixToIPNet(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"0.0.0.0/0", "0.0.0.0/0"},
		{"10.64.0.2/32", "10.64.0.2/32"},
		{"::/0", "::/0"},
		{"fc00:bbbb:bbbb:bb01::2/128", "fc00:bbbb:bbbb:bb01::2/128"},
	}
	for _, c := range cases {
		n := prefixToIPNet(netip.MustParsePrefix(c.in))
		if got := n.String(); got != c.want {
			t.Errorf("prefixToIPNet(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}
