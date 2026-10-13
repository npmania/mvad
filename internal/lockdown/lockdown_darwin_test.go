//go:build darwin

package lockdown

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
	ips  []netip.Addr
	want string
}{
	{
		name: "mixed",
		ips: []netip.Addr{
			netip.MustParseAddr("5.6.7.8"),
			netip.MustParseAddr("2001:db8::1"),
			netip.MustParseAddr("1.2.3.4"),
			netip.MustParseAddr("::ffff:1.2.3.4"),
		},
		want: `table <relays> const { 1.2.3.4, 5.6.7.8, 2001:db8::1 }
scrub in all fragment reassemble
pass quick on lo0 no state
pass out quick on utun99 keep state
` + link + `pass out quick to <relays> keep state
block return out quick all
block drop in quick all
`,
	},
	{
		name: "empty",
		want: `table <relays> const { }
scrub in all fragment reassemble
pass quick on lo0 no state
pass out quick on utun99 keep state
` + link + `pass out quick to <relays> keep state
block return out quick all
block drop in quick all
`,
	},
}

func TestBuildRules(t *testing.T) {
	for _, tt := range ruleTests {
		if got := buildRules(tt.ips, "utun99"); got != tt.want {
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
		cmd.Stdin = strings.NewReader(buildRules(tt.ips, "utun99"))
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Errorf("%s: pfctl -n: %v\n%s", tt.name, err, stderr.Bytes())
		}
	}
}
