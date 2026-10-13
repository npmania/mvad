package lockdown

import (
	"net/netip"
	"slices"
	"testing"
)

func TestSplitFamiliesDropsZones(t *testing.T) {
	ips := []netip.Addr{
		netip.MustParseAddr("185.213.154.66"),
		netip.MustParseAddr("fe80::1%lo0, 0.0.0.0/0, ::/0"),
		netip.MustParseAddr("2a03:1b20:3:f011::a01f"),
	}
	v4, v6 := splitFamilies(ips)
	if want := ips[:1]; !slices.Equal(v4, want) {
		t.Errorf("v4 = %v, want %v", v4, want)
	}
	if want := ips[2:]; !slices.Equal(v6, want) {
		t.Errorf("v6 = %v, want %v", v6, want)
	}
}
