//go:build linux

package firewall

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/npmania/mvad/internal/split"
)

const tableName = "mvad"

func up(c Config) error {
	if c.Iface == "" {
		return errors.New("firewall: empty iface")
	}
	if !c.Endpoint.IsValid() {
		return errors.New("firewall: invalid endpoint")
	}
	return runNft(buildScript(c))
}

func down() error {
	cmd := exec.Command("nft", "delete", "table", "inet", tableName)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	s := string(out)
	if strings.Contains(s, "No such file or directory") || strings.Contains(s, "does not exist") {
		return nil
	}
	return fmt.Errorf("nft delete table inet %s: %w: %s", tableName, err, bytes.TrimSpace(out))
}

func runNft(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft -f: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// writeLANRules opens the local network in one direction. dir is "daddr"
// for the output chain, "saddr" for input. It covers private and
// link-local unicast plus the multicast ranges that LAN service discovery
// (mDNS, SSDP) needs, for both IP families. IPv6 multicast is scoped to
// link-local (ff02::/16) so global-scope multicast stays inside the tunnel.
func writeLANRules(b *strings.Builder, dir string) {
	fmt.Fprintf(b, "\t\tip %s { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 } accept\n", dir)
	fmt.Fprintf(b, "\t\tip6 %s { fe80::/10, ff02::/16 } accept\n", dir)
}

func buildScript(c Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\n", tableName)
	fmt.Fprintf(&b, "delete table inet %s\n", tableName)
	fmt.Fprintf(&b, "table inet %s {\n", tableName)

	relay := c.Endpoint.Addr()
	port := c.Endpoint.Port()
	relayFam := "ip"
	if relay.Is6() {
		relayFam = "ip6"
	}
	proto := "udp"
	if c.TCP {
		proto = "tcp"
	}

	fmt.Fprintf(&b, "\tchain output {\n")
	b.WriteString("\t\ttype filter hook output priority 0; policy drop;\n")
	// Split-tunnel packets are still routed via the wg interface at this
	// point; re-routing happens after the chain. Save ct mark before the
	// wg accept terminates the chain so replies can be matched on input.
	fmt.Fprintf(&b, "\t\tmeta mark %#x ct mark set %#x\n", split.FWMark, split.FWMark)
	b.WriteString("\t\toifname \"lo\" accept\n")
	fmt.Fprintf(&b, "\t\toifname %q accept\n", c.Iface)
	fmt.Fprintf(&b, "\t\t%s dport %d %s daddr %s accept\n", proto, port, relayFam, relay)
	if c.AllowLAN {
		writeLANRules(&b, "daddr")
	}
	fmt.Fprintf(&b, "\t\tmeta mark %#x accept\n", split.FWMark)
	b.WriteString("\t}\n")

	fmt.Fprintf(&b, "\tchain input {\n")
	b.WriteString("\t\ttype filter hook input priority 0; policy drop;\n")
	b.WriteString("\t\tiifname \"lo\" accept\n")
	fmt.Fprintf(&b, "\t\tiifname %q accept\n", c.Iface)
	fmt.Fprintf(&b, "\t\t%s sport %d %s saddr %s accept\n", proto, port, relayFam, relay)
	if c.AllowLAN {
		writeLANRules(&b, "saddr")
	}
	fmt.Fprintf(&b, "\t\tct mark %#x accept\n", split.FWMark)
	b.WriteString("\t}\n")

	b.WriteString("}\n")
	return b.String()
}
