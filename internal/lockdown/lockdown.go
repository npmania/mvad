// Package lockdown installs a persistent nft inet table that drops
// non-Mullvad egress when no tunnel is up. The allow-list is the
// superset of all known Mullvad relay IPs from the cached relay list.
package lockdown

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

const (
	tableName  = "mvad-lockdown"
	scriptPath = "/var/lib/mvad/lockdown.nft"
	markerPath = "/run/mvad/lockdown"
)

var (
	ErrUnsupported = errors.New("lockdown: unsupported platform")
	ErrEmptyAllow  = errors.New("lockdown: refusing empty allow-list")
)

func On(relayIPs []netip.Addr) error {
	if !hasValid(relayIPs) {
		return ErrEmptyAllow
	}
	return on(relayIPs)
}

func Off() error { return off() }

func Refresh(relayIPs []netip.Addr) error {
	if !hasValid(relayIPs) {
		return ErrEmptyAllow
	}
	return refresh(relayIPs)
}

func Active() bool { return active() }

func hasValid(ips []netip.Addr) bool {
	for _, ip := range ips {
		if ip.IsValid() {
			return true
		}
	}
	return false
}

func buildScript(ips []netip.Addr) string {
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

func splitFamilies(ips []netip.Addr) (v4, v6 []netip.Addr) {
	seen := make(map[netip.Addr]bool)
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.IsValid() || seen[ip] {
			continue
		}
		seen[ip] = true
		if ip.Is4() {
			v4 = append(v4, ip)
		} else {
			v6 = append(v6, ip)
		}
	}
	sort.Slice(v4, func(i, j int) bool { return v4[i].Compare(v4[j]) < 0 })
	sort.Slice(v6, func(i, j int) bool { return v6[i].Compare(v6[j]) < 0 })
	return
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
