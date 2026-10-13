//go:build linux

package lockdown

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	tableName  = "mvad-lockdown"
	scriptPath = "/var/lib/mvad/lockdown.nft"
	markerPath = "/run/mvad/lockdown"
)

func on(relayIPs []netip.Addr, iface string) error {
	script := buildScript(relayIPs, iface)
	if err := writeScript(script); err != nil {
		return err
	}
	if err := runNft(script); err != nil {
		return err
	}
	writeMarker()
	return nil
}

func off() error {
	delErr := delTable()
	rmErr := os.Remove(scriptPath)
	if errors.Is(rmErr, os.ErrNotExist) {
		rmErr = nil
	}
	if delErr == nil {
		removeMarker()
	}
	_ = os.Remove(filepath.Dir(scriptPath))
	return errors.Join(delErr, rmErr)
}

func refresh(relayIPs []netip.Addr, iface string) error {
	if _, err := os.Stat(scriptPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	script := buildScript(relayIPs, iface)
	if err := writeScript(script); err != nil {
		return err
	}
	if !tablePresent() {
		return nil
	}
	if err := runNft(script); err != nil {
		return err
	}
	writeMarker()
	return nil
}

// The marker lives on tmpfs and misses tables installed by the boot
// scripts, so fall back to asking the kernel; unprivileged callers
// keep the marker-only answer.
func active() bool {
	if _, err := os.Stat(markerPath); err == nil {
		return true
	}
	return tablePresent()
}

func buildScript(ips []netip.Addr, iface string) string {
	v4, v6 := splitFamilies(ips)
	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\n", tableName)
	fmt.Fprintf(&b, "delete table inet %s\n", tableName)
	fmt.Fprintf(&b, "table inet %s {\n", tableName)
	writeSet(&b, "relays_v4", "ipv4_addr", v4)
	writeSet(&b, "relays_v6", "ipv6_addr", v6)
	b.WriteString("\tchain output {\n")
	b.WriteString("\t\ttype filter hook output priority 0; policy drop;\n")
	b.WriteString("\t\toifname \"lo\" accept\n")
	// Without this, a lockdown-plus-tunnel pairing passes the
	// handshake (relay daddr) but drops every new flow inside it.
	fmt.Fprintf(&b, "\t\toifname %q accept\n", iface)
	b.WriteString("\t\tct state established,related accept\n")
	b.WriteString("\t\tip daddr @relays_v4 accept\n")
	b.WriteString("\t\tip6 daddr @relays_v6 accept\n")
	b.WriteString("\t}\n")
	b.WriteString("\tchain input {\n")
	b.WriteString("\t\ttype filter hook input priority 0; policy drop;\n")
	b.WriteString("\t\tiifname \"lo\" accept\n")
	b.WriteString("\t\tct state established,related accept\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n")
	return b.String()
}

func writeSet(b *strings.Builder, name, typ string, ips []netip.Addr) {
	fmt.Fprintf(b, "\tset %s {\n", name)
	fmt.Fprintf(b, "\t\ttype %s\n", typ)
	b.WriteString("\t\tflags interval\n")
	if len(ips) > 0 {
		b.WriteString("\t\telements = {")
		for i, ip := range ips {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(b, " %s", ip)
		}
		b.WriteString(" }\n")
	}
	b.WriteString("\t}\n")
}

func runNft(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("lockdown: nft -f: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func delTable() error {
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
	return fmt.Errorf("lockdown: nft delete table inet %s: %w: %s", tableName, err, bytes.TrimSpace(out))
}

func tablePresent() bool {
	cmd := exec.Command("nft", "list", "table", "inet", tableName)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.Run() == nil
}
