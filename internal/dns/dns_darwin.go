//go:build darwin

package dns

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
)

// key names a service with no interface. An empty supplemental match
// domain makes its resolver the unscoped default. configd holds the
// key in memory only; a reboot clears it.
const key = "State:/Network/Service/mvad/DNS"

func set(_ string, servers []netip.Addr) error {
	if len(servers) == 0 {
		return errors.New("dns: no servers")
	}
	if err := scutil(setScript(servers), ""); err != nil {
		return err
	}
	flush()
	return nil
}

func restore(string) error {
	if err := scutil("open\nremove "+key+"\nquit\n", "No such key"); err != nil {
		return err
	}
	flush()
	return nil
}

func setScript(servers []netip.Addr) string {
	var b strings.Builder
	b.WriteString("open\nd.init\nd.add ServerAddresses *")
	for _, s := range servers {
		b.WriteString(" " + s.String())
	}
	b.WriteString("\nd.add SupplementalMatchDomains * \"\"\nset " + key + "\nquit\n")
	return b.String()
}

// scutil runs script through scutil(8), which exits 0 even on failure
// and prints the error instead; any output other than benign is an
// error.
func scutil(script, benign string) error {
	cmd := exec.Command("/usr/sbin/scutil")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return fmt.Errorf("scutil: %w: %s", err, s)
	}
	if s != "" && s != benign {
		return fmt.Errorf("scutil: %s", s)
	}
	return nil
}

func flush() {
	_ = exec.Command("/usr/bin/dscacheutil", "-flushcache").Run()
	_ = exec.Command("/usr/bin/killall", "-HUP", "mDNSResponder").Run()
}
