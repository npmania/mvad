// Package lockdown installs persistent firewall rules that drop
// non-Mullvad egress when no tunnel is up. The allow-list is the
// superset of all known Mullvad relay IPs from the cached relay list.
package lockdown

import (
	"errors"
	"net/netip"
	"sort"
)

var (
	ErrUnsupported = errors.New("lockdown: unsupported platform")
	ErrEmptyAllow  = errors.New("lockdown: refusing empty allow-list")
)

func On(relayIPs []netip.Addr, iface string) error {
	if !hasValid(relayIPs) {
		return ErrEmptyAllow
	}
	return on(relayIPs, iface)
}

func Off() error { return off() }

func Refresh(relayIPs []netip.Addr, iface string) error {
	if !hasValid(relayIPs) {
		return ErrEmptyAllow
	}
	return refresh(relayIPs, iface)
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

func splitFamilies(ips []netip.Addr) (v4, v6 []netip.Addr) {
	seen := make(map[netip.Addr]bool)
	for _, ip := range ips {
		ip = ip.Unmap()
		// A zone is free text that lands verbatim in the root-loaded
		// ruleset; relays never have one.
		if !ip.IsValid() || ip.Zone() != "" || seen[ip] {
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
