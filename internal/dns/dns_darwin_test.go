//go:build darwin

package dns

import (
	"net/netip"
	"testing"
)

func TestSetScript(t *testing.T) {
	tests := []struct {
		servers []string
		want    string
	}{
		{
			[]string{"10.64.0.1"},
			"open\nd.init\nd.add ServerAddresses * 10.64.0.1\nd.add SupplementalMatchDomains * \"\"\nset State:/Network/Service/mvad/DNS\nquit\n",
		},
		{
			[]string{"10.64.0.1", "fc00:bbbb:bbbb:bb01::1"},
			"open\nd.init\nd.add ServerAddresses * 10.64.0.1 fc00:bbbb:bbbb:bb01::1\nd.add SupplementalMatchDomains * \"\"\nset State:/Network/Service/mvad/DNS\nquit\n",
		},
	}
	for _, tt := range tests {
		var servers []netip.Addr
		for _, s := range tt.servers {
			servers = append(servers, netip.MustParseAddr(s))
		}
		if got := setScript(servers); got != tt.want {
			t.Errorf("setScript(%v) =\n%s\nwant:\n%s", tt.servers, got, tt.want)
		}
	}
}
