//go:build darwin

package pf

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// LinkRules pass the DHCP, DHCPv6 and NDP traffic that keeps the
// machine's addresses. IPConfiguration, root inside configd, takes
// DHCP replies on a UDP socket and sends router solicitations from
// user space; inbound NDP always meets pf. user root: binding port 68
// needs no privilege on macOS.
const LinkRules = `pass out quick inet proto udp from any port 68 to any port 67 user root no state
pass in quick inet proto udp from any port 67 to any port 68 user root no state
pass out quick inet6 proto udp from fe80::/10 port 546 to { ff02::1:2, ff05::1:3 } port 547 no state
pass in quick inet6 proto udp from fe80::/10 port 547 to fe80::/10 port 546 no state
pass out quick inet6 proto icmp6 to ff02::2 icmp6-type routersol no state
pass out quick inet6 proto icmp6 icmp6-type { neighbrsol, neighbradv } no state
pass in quick inet6 proto icmp6 from fe80::/10 icmp6-type { routeradv, neighbrsol, redir } no state
pass in quick inet6 proto icmp6 icmp6-type neighbradv no state
`

// pfctl prints this on every -f, even under -q.
const flushWarning = "pfctl: Use of -f option, could result in flushing of rules\n" +
	"present in the main ruleset added by the system at startup.\n" +
	"See /etc/pf.conf for further details.\n"

// Load atomically replaces the rules in anchor.
func Load(anchor, rules string) error {
	_, _, err := pfctl(rules, "-q", "-a", anchor, "-f", "-")
	return err
}

// Flush removes the rules and tables in anchor. A missing anchor is
// not an error.
func Flush(anchor string) error {
	_, _, err := pfctl("", "-q", "-a", anchor, "-F", "all")
	return err
}

// HasRules reports whether anchor holds filter rules. It needs root.
func HasRules(anchor string) bool {
	out, _, err := pfctl("", "-q", "-a", anchor, "-s", "rules")
	return err == nil && len(bytes.TrimSpace(out)) > 0
}

// Enabled reports whether pf is running. It needs root.
func Enabled() bool {
	out, _, err := pfctl("", "-q", "-s", "info")
	return err == nil && bytes.HasPrefix(out, []byte("Status: Enabled"))
}

// CheckHook reports an error if the main ruleset lacks the
// anchor "com.apple/*" rule; without it com.apple anchors load but
// never run.
func CheckHook() error {
	out, _, err := pfctl("", "-q", "-s", "rules")
	if err != nil {
		return err
	}
	if !hasHook(out) {
		return errors.New(`pf: main ruleset lacks anchor "com.apple/*" (run pfctl -f /etc/pf.conf as root)`)
	}
	return nil
}

// Enable takes a pf enable reference, records its token in file and
// releases the reference file recorded before. Each file holds exactly
// one reference.
func Enable(file string) error {
	prev, _ := os.ReadFile(file)
	// -q would suppress the token.
	_, stderr, err := pfctl("", "-E")
	if err != nil {
		return err
	}
	tok, ok := parseToken(stderr)
	if !ok {
		return fmt.Errorf("pfctl -E: no token: %s", bytes.TrimSpace(stderr))
	}
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		release(tok)
		return err
	}
	if err := os.WriteFile(file, []byte(tok+"\n"), 0600); err != nil {
		release(tok)
		return err
	}
	// A foreign pfctl -d frees every token, after which the kernel
	// may mint the old value again.
	if old := strings.TrimSpace(string(prev)); old != "" && old != tok {
		return release(old)
	}
	return nil
}

// Release drops the reference recorded in file and removes file. A
// missing file or a token pf no longer knows is not an error.
func Release(file string) error {
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if tok := strings.TrimSpace(string(b)); tok != "" {
		if err := release(tok); err != nil {
			return err
		}
	}
	return os.Remove(file)
}

// A token dies with a reboot or a pfctl -d, after which releasing it
// is a no-op.
func release(tok string) error {
	_, stderr, err := pfctl("", "-q", "-X", tok)
	if err != nil && !bytes.Contains(stderr, []byte("token invalid")) && !bytes.Contains(stderr, []byte("pf not enabled")) {
		return err
	}
	return nil
}

func pfctl(stdin string, args ...string) (stdout, stderr []byte, err error) {
	var o, e bytes.Buffer
	cmd := exec.Command("/sbin/pfctl", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Stdout = &o
	cmd.Stderr = &e
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if err := cmd.Run(); err != nil {
		msg := bytes.Replace(e.Bytes(), []byte(flushWarning), nil, 1)
		return o.Bytes(), e.Bytes(), fmt.Errorf("pfctl %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(msg))
	}
	return o.Bytes(), e.Bytes(), nil
}

func parseToken(out []byte) (string, bool) {
	for l := range strings.Lines(string(out)) {
		if s, ok := strings.CutPrefix(l, "Token : "); ok {
			s = strings.TrimSpace(s)
			if _, err := strconv.ParseUint(s, 10, 64); err == nil {
				return s, true
			}
		}
	}
	return "", false
}

func hasHook(rules []byte) bool {
	for l := range strings.Lines(string(rules)) {
		if strings.HasPrefix(l, `anchor "com.apple/*"`) {
			return true
		}
	}
	return false
}
