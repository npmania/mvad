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
	}, "enp0s3")
	wants := []string{
		"add table ip mvad-split",
		"delete table ip mvad-split",
		"table ip mvad-split {",
		"type route hook output priority -150;",
		"ip daddr 10.64.0.1 return",
		`socket cgroupv2 level 1 "mvad-split" meta mark set 0xca6c`,
		"type nat hook postrouting priority srcnat;",
		`meta mark 0xca6c oifname "enp0s3" masquerade`,
	}
	if strings.Contains(got, "fc00:bbbb::1") {
		t.Errorf("v6 address leaked into ip-family script\n--- got ---\n%s", got)
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("script missing %q\n--- got ---\n%s", w, got)
		}
	}
}
