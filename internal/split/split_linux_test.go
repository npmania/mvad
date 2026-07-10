//go:build linux

package split

import (
	"net/netip"
	"strings"
	"testing"
)

func TestBuildScript(t *testing.T) {
	got := buildScript(Config{
		Gateway: netip.MustParseAddr("192.168.1.1"),
		Dev:     "enp0s3",
		DNS: []netip.Addr{
			netip.MustParseAddr("10.64.0.1"),
			netip.MustParseAddr("fc00:bbbb::1"),
		},
		Nets: []netip.Prefix{
			netip.MustParsePrefix("172.18.0.0/16"),
			netip.MustParsePrefix("fd00::/64"),
		},
	})
	wants := []string{
		"add table ip mvad-split",
		"delete table ip mvad-split",
		"table ip mvad-split {",
		"add table ip6 mvad-split",
		"delete table ip6 mvad-split",
		"table ip6 mvad-split {",
		"type route hook output priority -150;",
		"ip daddr 10.64.0.1 return",
		"ip6 daddr fc00:bbbb::1 return",
		`socket cgroupv2 level 1 "mvad-split" meta mark set 0xca6c`,
		"type filter hook prerouting priority -150;",
		"ct mark 0xca6c meta mark set 0xca6c return",
		"ip saddr @net meta mark set 0xca6c",
		"ip6 saddr @net meta mark set 0xca6c",
		"meta mark 0xca6c ct mark set 0xca6c",
		"elements = { 172.18.0.0/16 }",
		"elements = { fd00::/64 }",
		"type nat hook postrouting priority srcnat;",
		`meta mark 0xca6c oifname "enp0s3" masquerade`,
	}
	v4Table, v6Table, ok := strings.Cut(got, "table ip6 mvad-split")
	if !ok {
		t.Fatalf("ip6 table missing\n--- got ---\n%s", got)
	}
	if strings.Contains(v4Table, "ip6 daddr") || strings.Contains(v4Table, "fd00::/64") {
		t.Errorf("v6 address leaked into ip-family script\n--- got ---\n%s", got)
	}
	if strings.Contains(v6Table, "ip daddr ") || strings.Contains(v6Table, "172.18.0.0/16") {
		t.Errorf("v4 address leaked into ip6-family script\n--- got ---\n%s", got)
	}
	if strings.Contains(got, "dnat") {
		t.Errorf("dnat rule in full-tunnel script\n--- got ---\n%s", got)
	}
	if strings.Contains(got, "ct direction reply return") {
		t.Errorf("reply exemption in full-tunnel script; replies from tagged sources need the mark\n--- got ---\n%s", got)
	}
	if strings.Contains(got, "drop") {
		t.Errorf("guard rule in full-tunnel script\n--- got ---\n%s", got)
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("script missing %q\n--- got ---\n%s", w, got)
		}
	}
}

func TestCoalesce(t *testing.T) {
	got := coalesce([]netip.Prefix{
		netip.MustParsePrefix("172.18.0.2/32"),
		netip.MustParsePrefix("172.18.0.0/16"),
		netip.MustParsePrefix("172.18.0.2/32"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("fd00::1/128"),
		netip.MustParsePrefix("fd00::/64"),
	})
	want := []netip.Prefix{
		netip.MustParsePrefix("172.18.0.0/16"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("fd00::/64"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestBuildScriptCoalescesSeed(t *testing.T) {
	got := buildScript(Config{
		Split: true,
		Iface: "mvad-wg0",
		DNS:   []netip.Addr{netip.MustParseAddr("10.64.0.1")},
		Nets: []netip.Prefix{
			netip.MustParsePrefix("172.18.0.2/32"),
			netip.MustParsePrefix("172.18.0.0/16"),
		},
	})
	if !strings.Contains(got, "elements = { 172.18.0.0/16 }") {
		t.Errorf("seed not coalesced\n--- got ---\n%s", got)
	}
	if strings.Contains(got, "172.18.0.2/32") {
		t.Errorf("covered prefix left in seed\n--- got ---\n%s", got)
	}
}

func TestBuildScriptSplit(t *testing.T) {
	got := buildScript(Config{
		Split: true,
		Iface: "mvad-wg0",
		DNS: []netip.Addr{
			netip.MustParseAddr("10.64.0.1"),
			netip.MustParseAddr("2a07:e340::2"),
		},
		Nets: []netip.Prefix{netip.MustParsePrefix("172.18.0.2/32")},
	})
	wants := []string{
		"ct direction reply return",
		`socket cgroupv2 level 1 "mvad-split" meta mark set 0xca6c`,
		"meta mark 0xca6c ct mark set 0xca6c",
		"ct mark 0xca6c meta mark set 0xca6c return",
		"ip saddr @net meta mark set 0xca6c",
		"elements = { 172.18.0.2/32 }",
		"type nat hook output priority -100;",
		"type nat hook prerouting priority dstnat;",
		"ip daddr @net accept",
		"ip daddr 127.0.0.0/8 accept",
		"meta mark 0xca6c udp dport 53 dnat to 10.64.0.1",
		"meta mark 0xca6c tcp dport 53 dnat to 10.64.0.1",
		"ip6 daddr @net accept",
		"ip6 daddr ::1 accept",
		"meta mark 0xca6c udp dport 53 dnat to 2a07:e340::2",
		"meta mark 0xca6c tcp dport 53 dnat to 2a07:e340::2",
		"type filter hook forward priority 0;",
		`oifname "lo" accept`,
		`oifname "mvad-wg0" accept`,
		"ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 } accept",
		"ip6 daddr { fe80::/10, fc00::/7, ff02::/16 } accept",
		"meta mark 0xca6c drop",
		`meta mark 0xca6c oifname "mvad-wg0" masquerade`,
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("script missing %q\n--- got ---\n%s", w, got)
		}
	}
	if strings.Contains(got, "daddr 10.64.0.1 return") {
		t.Errorf("tunnel-DNS exemption in split script\n--- got ---\n%s", got)
	}
	if strings.Contains(got, "reject") {
		t.Errorf("reject left in split script; DNS is rewritten now\n--- got ---\n%s", got)
	}
	v4Table, v6Table, ok := strings.Cut(got, "table ip6 mvad-split")
	if !ok {
		t.Fatalf("ip6 table missing\n--- got ---\n%s", got)
	}
	if strings.Contains(v4Table, "2a07:e340::2") {
		t.Errorf("v6 dnat leaked into ip-family script\n--- got ---\n%s", got)
	}
	if strings.Contains(v6Table, "dnat to 10.64.0.1") {
		t.Errorf("v4 dnat leaked into ip6-family script\n--- got ---\n%s", got)
	}
}

func TestParseSetElems(t *testing.T) {
	data := []byte(`{"nftables":[{"metainfo":{}},{"set":{"family":"ip","name":"net",
		"elem":["10.5.0.7",{"prefix":{"addr":"172.18.0.0","len":16}},
		{"elem":{"val":{"prefix":{"addr":"192.0.2.0","len":24}},"expires":10}}]}}]}`)
	got := parseSetElems(data)
	want := []netip.Prefix{
		netip.MustParsePrefix("10.5.0.7/32"),
		netip.MustParsePrefix("172.18.0.0/16"),
		netip.MustParsePrefix("192.0.2.0/24"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
