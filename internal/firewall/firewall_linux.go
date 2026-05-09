//go:build linux

package firewall

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

	fmt.Fprintf(&b, "\tchain output {\n")
	b.WriteString("\t\ttype filter hook output priority 0; policy drop;\n")
	b.WriteString("\t\toifname \"lo\" accept\n")
	fmt.Fprintf(&b, "\t\toifname %q accept\n", c.Iface)
	fmt.Fprintf(&b, "\t\tudp dport %d %s daddr %s accept\n", port, relayFam, relay)
	for _, d := range c.DNS {
		fam := "ip"
		if d.Is6() {
			fam = "ip6"
		}
		fmt.Fprintf(&b, "\t\tudp dport 53 %s daddr %s accept\n", fam, d)
		fmt.Fprintf(&b, "\t\ttcp dport 53 %s daddr %s accept\n", fam, d)
	}
	if c.AllowLAN {
		b.WriteString("\t\tip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16 } accept\n")
	}
	b.WriteString("\t}\n")

	fmt.Fprintf(&b, "\tchain input {\n")
	b.WriteString("\t\ttype filter hook input priority 0; policy drop;\n")
	b.WriteString("\t\tiifname \"lo\" accept\n")
	fmt.Fprintf(&b, "\t\tiifname %q accept\n", c.Iface)
	fmt.Fprintf(&b, "\t\tudp sport %d %s saddr %s accept\n", port, relayFam, relay)
	if c.AllowLAN {
		b.WriteString("\t\tip saddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16 } accept\n")
	}
	b.WriteString("\t}\n")

	b.WriteString("}\n")
	return b.String()
}
