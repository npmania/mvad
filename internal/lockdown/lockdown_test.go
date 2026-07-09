package lockdown

import (
	"net/netip"
	"testing"
)

func TestBuildScript(t *testing.T) {
	ips := []netip.Addr{
		netip.MustParseAddr("5.6.7.8"),
		netip.MustParseAddr("1.2.3.4"),
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("1.2.3.4"),
	}
	got := buildScript(ips, "mvad-wg0")
	want := `add table inet mvad-lockdown
delete table inet mvad-lockdown
table inet mvad-lockdown {
	set relays_v4 {
		type ipv4_addr
		flags interval
		elements = { 1.2.3.4, 5.6.7.8 }
	}
	set relays_v6 {
		type ipv6_addr
		flags interval
		elements = { 2001:db8::1 }
	}
	chain output {
		type filter hook output priority 0; policy drop;
		oifname "lo" accept
		oifname "mvad-wg0" accept
		ct state established,related accept
		ip daddr @relays_v4 accept
		ip6 daddr @relays_v6 accept
	}
	chain input {
		type filter hook input priority 0; policy drop;
		iifname "lo" accept
		ct state established,related accept
	}
}
`
	if got != want {
		t.Errorf("buildScript mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildScriptEmpty(t *testing.T) {
	got := buildScript(nil, "mvad-wg0")
	want := `add table inet mvad-lockdown
delete table inet mvad-lockdown
table inet mvad-lockdown {
	set relays_v4 {
		type ipv4_addr
		flags interval
	}
	set relays_v6 {
		type ipv6_addr
		flags interval
	}
	chain output {
		type filter hook output priority 0; policy drop;
		oifname "lo" accept
		oifname "mvad-wg0" accept
		ct state established,related accept
		ip daddr @relays_v4 accept
		ip6 daddr @relays_v6 accept
	}
	chain input {
		type filter hook input priority 0; policy drop;
		iifname "lo" accept
		ct state established,related accept
	}
}
`
	if got != want {
		t.Errorf("buildScript mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}
