//go:build darwin

package lockdown

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/npmania/mvad/internal/pf"
)

const (
	anchor     = "com.apple/mvad-lockdown"
	scriptPath = "/var/db/mvad/lockdown.pf"
	markerPath = "/var/run/mvad/lockdown"
	tokenFile  = "/var/run/mvad/lockdown.token"
)

func on(relayIPs []netip.Addr, iface string) error {
	if err := pf.CheckHook(); err != nil {
		return err
	}
	rules := buildRules(relayIPs, iface)
	if err := pf.Load(anchor, rules); err != nil {
		return err
	}
	// The script goes last: the boot job loads whatever it finds.
	if err := pf.Enable(tokenFile); err != nil {
		off()
		return err
	}
	if err := writeScript(rules); err != nil {
		off()
		return err
	}
	writeMarker()
	return nil
}

func off() error {
	flushErr := pf.Flush(anchor)
	relErr := pf.Release(tokenFile)
	rmErr := os.Remove(scriptPath)
	if errors.Is(rmErr, os.ErrNotExist) {
		rmErr = nil
	}
	if flushErr == nil {
		removeMarker()
	}
	_ = os.Remove(filepath.Dir(scriptPath))
	return errors.Join(flushErr, relErr, rmErr)
}

func refresh(relayIPs []netip.Addr, iface string) error {
	if _, err := os.Stat(scriptPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	rules := buildRules(relayIPs, iface)
	if err := writeScript(rules); err != nil {
		return err
	}
	if !pf.HasRules(anchor) {
		return nil
	}
	if err := pf.Load(anchor, rules); err != nil {
		return err
	}
	writeMarker()
	return nil
}

// Without the marker, ask pf: a boot job that writes none may have
// loaded the rules. Unprivileged callers get the marker-only answer.
func active() bool {
	if _, err := os.Stat(markerPath); err == nil {
		return true
	}
	return pf.HasRules(anchor) && pf.Enabled()
}

func buildRules(ips []netip.Addr, iface string) string {
	v4, v6 := splitFamilies(ips)
	var b strings.Builder
	b.WriteString("table <relays> const {")
	for i, ip := range append(v4, v6...) {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, " %s", ip)
	}
	b.WriteString(" }\n")
	b.WriteString("scrub in all fragment reassemble\n")
	b.WriteString("pass quick on lo0 no state\n")
	fmt.Fprintf(&b, "pass out quick on %s keep state\n", iface)
	b.WriteString(pf.LinkRules)
	b.WriteString("pass out quick to <relays> keep state\n")
	b.WriteString("block return out quick all\n")
	b.WriteString("block drop in quick all\n")
	return b.String()
}
