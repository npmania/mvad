//go:build darwin

package firewall

import (
	"bytes"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/npmania/mvad/internal/pf"
)

const link = pf.LinkRules

var ruleTests = []struct {
	name string
	c    Config
	want string
}{
	{
		name: "udp4-lan",
		c: Config{
			Iface:    "utun99",
			Endpoint: netip.MustParseAddrPort("185.65.135.100:51820"),
			AllowLAN: true,
		},
		want: `scrub in all fragment reassemble
pass quick on lo0 no state
pass quick on utun99 no state
` + link + `pass out quick inet proto udp from any to 185.65.135.100 port 51820 keep state
pass in quick inet proto udp from 185.65.135.100 port 51820 to any keep state
pass out quick inet from any to { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 } no state
pass in quick inet from { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 } to any no state
pass out quick inet6 from any to { fe80::/10, ff02::/16 } no state
pass in quick inet6 from { fe80::/10, ff02::/16 } to any no state
block return out quick all
block drop in quick all
`,
	},
	{
		name: "tcp6",
		c: Config{
			Iface:    "utun99",
			Endpoint: netip.MustParseAddrPort("[2a03:1b20:3:f011::a01f]:443"),
			TCP:      true,
		},
		want: `scrub in all fragment reassemble
pass quick on lo0 no state
pass quick on utun99 no state
` + link + `pass out quick inet6 proto tcp from any to 2a03:1b20:3:f011::a01f port 443 keep state
pass in quick inet6 proto tcp from 2a03:1b20:3:f011::a01f port 443 to any keep state
block return out quick all
block drop in quick all
`,
	},
	{
		name: "mapped4",
		c: Config{
			Iface:    "utun99",
			Endpoint: netip.MustParseAddrPort("[::ffff:185.65.135.100]:51820"),
		},
		want: `scrub in all fragment reassemble
pass quick on lo0 no state
pass quick on utun99 no state
` + link + `pass out quick inet proto udp from any to 185.65.135.100 port 51820 keep state
pass in quick inet proto udp from 185.65.135.100 port 51820 to any keep state
block return out quick all
block drop in quick all
`,
	},
}

func TestBuildRules(t *testing.T) {
	for _, tt := range ruleTests {
		if got := buildRules(tt.c); got != tt.want {
			t.Errorf("%s: buildRules mismatch\ngot:\n%s\nwant:\n%s", tt.name, got, tt.want)
		}
	}
}

func TestRulesParse(t *testing.T) {
	if _, err := os.Stat("/sbin/pfctl"); err != nil {
		t.Skip("no pfctl")
	}
	for _, tt := range ruleTests {
		cmd := exec.Command("/sbin/pfctl", "-n", "-a", "com.apple/mvadtest", "-f", "-")
		cmd.Stdin = strings.NewReader(buildRules(tt.c))
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Errorf("%s: pfctl -n: %v\n%s", tt.name, err, stderr.Bytes())
		}
	}
}
