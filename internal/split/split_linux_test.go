//go:build linux

package split

import (
	"net/netip"
	"strings"
	"testing"
)

func TestBuildScript(t *testing.T) {
	got := buildScript([]netip.Addr{
		netip.MustParseAddr("10.64.0.1"),
		netip.MustParseAddr("fc00:bbbb::1"),
	})
	wants := []string{
		"add table inet mvad-split",
		"delete table inet mvad-split",
		"table inet mvad-split {",
		"type route hook output priority -150;",
		"ip daddr 10.64.0.1 return",
		"ip6 daddr fc00:bbbb::1 return",
		`socket cgroupv2 level 1 "mvad-split" meta mark set 0xca6c`,
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("script missing %q\n--- got ---\n%s", w, got)
		}
	}
}
