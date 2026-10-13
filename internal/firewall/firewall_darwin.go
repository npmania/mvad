//go:build darwin

package firewall

import (
	"errors"
	"fmt"
	"strings"

	"github.com/npmania/mvad/internal/pf"
)

const (
	anchor    = "com.apple/mvad"
	tokenFile = "/var/run/mvad/firewall.token"
)

func up(c Config) error {
	if c.Iface == "" {
		return errors.New("firewall: empty iface")
	}
	if !c.Endpoint.IsValid() {
		return errors.New("firewall: invalid endpoint")
	}
	if err := pf.CheckHook(); err != nil {
		return err
	}
	if err := pf.Load(anchor, buildRules(c)); err != nil {
		return err
	}
	// The caller tears down without firewall.Down when Up fails.
	if err := pf.Enable(tokenFile); err != nil {
		pf.Flush(anchor)
		return err
	}
	return nil
}

func down() error {
	return errors.Join(pf.Flush(anchor), pf.Release(tokenFile))
}

func installed() bool { return pf.HasRules(anchor) }

const (
	lan4 = "{ 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 }"
	lan6 = "{ fe80::/10, ff02::/16 }"
)

func buildRules(c Config) string {
	relay := c.Endpoint.Addr().Unmap()
	port := c.Endpoint.Port()
	fam := "inet"
	if relay.Is6() {
		fam = "inet6"
	}
	proto := "udp"
	if c.TCP {
		proto = "tcp"
	}

	var b strings.Builder
	b.WriteString("scrub in all fragment reassemble\n")
	b.WriteString("pass quick on lo0 no state\n")
	fmt.Fprintf(&b, "pass quick on %s no state\n", c.Iface)
	b.WriteString(pf.LinkRules)
	fmt.Fprintf(&b, "pass out quick %s proto %s from any to %s port %d keep state\n", fam, proto, relay, port)
	fmt.Fprintf(&b, "pass in quick %s proto %s from %s port %d to any keep state\n", fam, proto, relay, port)
	if c.AllowLAN {
		fmt.Fprintf(&b, "pass out quick inet from any to %s no state\n", lan4)
		fmt.Fprintf(&b, "pass in quick inet from %s to any no state\n", lan4)
		fmt.Fprintf(&b, "pass out quick inet6 from any to %s no state\n", lan6)
		fmt.Fprintf(&b, "pass in quick inet6 from %s to any no state\n", lan6)
	}
	b.WriteString("block return out quick all\n")
	b.WriteString("block drop in quick all\n")
	return b.String()
}
