//go:build linux

package split

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type state struct {
	Split    bool       `json:"split,omitempty"`
	Gateway  netip.Addr `json:"gateway"`
	Gateway6 netip.Addr `json:"gateway6"`
	Dev      string     `json:"dev"`
	Iface    string     `json:"iface,omitempty"`
	// The src_valid_mark value found before mvad first set it, carried
	// across reconnects so teardown can put it back.
	SrcValidMark string `json:"src_valid_mark,omitempty"`
}

const srcValidMarkPath = "/proc/sys/net/ipv4/conf/all/src_valid_mark"

func readState() (state, bool) {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return state{}, false
	}
	var s state
	if json.Unmarshal(data, &s) != nil {
		return state{}, false
	}
	return s, true
}

func up(c Config) error {
	if !cgroupV2Mounted() {
		return errors.New("split: cgroup v2 unified hierarchy not mounted at /sys/fs/cgroup")
	}
	if c.Split {
		if c.Iface == "" {
			return errors.New("split: empty iface")
		}
	} else {
		if !c.Gateway.IsValid() {
			return errors.New("split: invalid gateway")
		}
		if c.Dev == "" {
			return errors.New("split: empty device")
		}
	}
	if err := os.MkdirAll(cgroupDir, 0755); err != nil {
		return fmt.Errorf("split: mkdir %s: %w", cgroupDir, err)
	}
	// When replacing a split session with another, a failed install
	// must not strip what remains of the old one: its rules,
	// unreachable defaults, and tables (nft -f is atomic) still hold
	// the set fail-closed.
	old, hadState := readState()
	prevSplit := hadState && old.Split && c.Split
	prior := old.SrcValidMark
	if prior == "" {
		if data, err := os.ReadFile(srcValidMarkPath); err == nil {
			prior = strings.TrimSpace(string(data))
		}
	}
	rollback := func() {
		if prevSplit {
			return
		}
		_ = delRule()
		_ = delRoute()
		_ = nftDel()
		_ = removeState()
		restoreSrcValidMark(state{Iface: c.Iface, SrcValidMark: prior})
	}
	if err := saveState(state{Split: c.Split, Gateway: c.Gateway, Gateway6: c.Gateway6, Dev: c.Dev, Iface: c.Iface, SrcValidMark: prior}); err != nil {
		return err
	}
	if err := runNft(buildScript(c)); err != nil {
		rollback()
		return fmt.Errorf("split: install nft: %w", err)
	}
	if err := installRoutes(c); err != nil {
		rollback()
		return err
	}
	// Reply packets get their mark restored from conntrack in
	// prerouting, but rp_filter validates them against an unmarked
	// lookup unless src_valid_mark is set; down puts back the recorded
	// prior value.
	if err := os.WriteFile(srcValidMarkPath, []byte("1"), 0644); err != nil {
		rollback()
		return fmt.Errorf("split: set src_valid_mark: %w", err)
	}
	return nil
}

func down() error {
	if _, err := os.Stat(stateFile); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// A corrupt state file still tears down; only the sysctl restore
	// needs the recorded value.
	s, _ := readState()
	err := errors.Join(delRule(), delRoute(), nftDel(), removeState())
	restoreSrcValidMark(s)
	// Gone only when no member pids remain; occupied membership
	// survives for the next connect.
	_ = os.Remove(cgroupDir)
	return err
}

// restoreSrcValidMark puts the sysctl back as recorded — unless
// another wireguard interface is live: wg-quick asserts it once at up
// and its tunnel goes deaf under strict rp_filter without it.
func restoreSrcValidMark(s state) {
	if s.SrcValidMark != "0" || otherWireguard(s.Iface) {
		return
	}
	_ = os.WriteFile(srcValidMarkPath, []byte("0"), 0644)
}

func otherWireguard(self string) bool {
	cmd := exec.Command("ip", "-j", "link", "show", "type", "wireguard")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return true
	}
	var links []struct {
		Ifname string `json:"ifname"`
	}
	if json.Unmarshal(out, &links) != nil {
		return true
	}
	for _, l := range links {
		if l.Ifname != self {
			return true
		}
	}
	return false
}

func addPID(pid int) error {
	if !available() {
		return ErrUnavailable
	}
	if pid <= 0 {
		return fmt.Errorf("split: invalid pid %d", pid)
	}
	return writeProcs(filepath.Join(cgroupDir, "cgroup.procs"), pid)
}

func rmPID(pid int) error {
	if !available() {
		return ErrUnavailable
	}
	if pid <= 0 {
		return fmt.Errorf("split: invalid pid %d", pid)
	}
	return writeProcs(filepath.Join(cgroupRoot, "cgroup.procs"), pid)
}

func listPIDs() ([]int, error) {
	if !cgroupExists() {
		return nil, ErrUnavailable
	}
	data, err := os.ReadFile(filepath.Join(cgroupDir, "cgroup.procs"))
	if err != nil {
		return nil, fmt.Errorf("split: read cgroup.procs: %w", err)
	}
	var pids []int
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			return nil, fmt.Errorf("split: parse pid %q: %w", line, err)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// setNets replaces the live sets with ps in one nft transaction.
func setNets(ps []netip.Prefix) error {
	if !available() {
		return ErrUnavailable
	}
	ps = coalesce(ps)
	var b strings.Builder
	for _, fam := range []string{"ip", "ip6"} {
		v6 := fam == "ip6"
		fmt.Fprintf(&b, "flush set %s %s %s\n", fam, tableName, setName)
		var elems []string
		for _, p := range ps {
			if p.Addr().Is6() != v6 {
				continue
			}
			elems = append(elems, p.String())
		}
		if len(elems) > 0 {
			fmt.Fprintf(&b, "add element %s %s %s { %s }\n", fam, tableName, setName, strings.Join(elems, ", "))
		}
	}
	return runNft(b.String())
}

// liveNets reads the current set elements, so a reconnect can keep
// protecting them when container resolution fails.
func liveNets() []netip.Prefix {
	var out []netip.Prefix
	for _, fam := range []string{"ip", "ip6"} {
		cmd := exec.Command("nft", "-j", "list", "set", fam, tableName, setName)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		data, err := cmd.Output()
		if err != nil {
			continue
		}
		out = append(out, parseSetElems(data)...)
	}
	return out
}

func parseSetElems(data []byte) []netip.Prefix {
	var doc struct {
		Nftables []struct {
			Set struct {
				Elem []any `json:"elem"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	var out []netip.Prefix
	for _, n := range doc.Nftables {
		for _, e := range n.Set.Elem {
			if p, ok := elemPrefix(e); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

func elemPrefix(e any) (netip.Prefix, bool) {
	switch v := e.(type) {
	case string:
		if a, err := netip.ParseAddr(v); err == nil {
			return netip.PrefixFrom(a, a.BitLen()), true
		}
	case map[string]any:
		if inner, ok := v["elem"].(map[string]any); ok {
			return elemPrefix(inner["val"])
		}
		if pr, ok := v["prefix"].(map[string]any); ok {
			addr, _ := pr["addr"].(string)
			bits, _ := pr["len"].(float64)
			if a, err := netip.ParseAddr(addr); err == nil {
				if p, err := a.Prefix(int(bits)); err == nil {
					return p, true
				}
			}
		}
	}
	return netip.Prefix{}, false
}

// coalesce drops prefixes covered by another so the seeded set never
// declares conflicting intervals.
func coalesce(ps []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for i, p := range ps {
		covered := false
		for j, q := range ps {
			if i == j || p.Addr().Is6() != q.Addr().Is6() {
				continue
			}
			if q.Bits() < p.Bits() && q.Contains(p.Addr()) {
				covered = true
				break
			}
			if q == p && j < i {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

func clear() error {
	var errs []error
	if cgroupExists() {
		pids, err := listPIDs()
		if err != nil {
			return err
		}
		root := filepath.Join(cgroupRoot, "cgroup.procs")
		for _, pid := range pids {
			if err := writeProcs(root, pid); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				errs = append(errs, fmt.Errorf("split: move pid %d: %w", pid, err))
			}
		}
	}
	for _, fam := range []string{"ip", "ip6"} {
		err := run("nft", "flush", "set", fam, tableName, setName)
		if err != nil && !notFound(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func available() bool {
	_, err := os.Stat(stateFile)
	return err == nil
}

func splitMode() bool {
	s, ok := readState()
	return ok && s.Split
}

func cgroupExists() bool {
	_, err := os.Stat(filepath.Join(cgroupDir, "cgroup.procs"))
	return err == nil
}

func cgroupV2Mounted() bool {
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	return err == nil
}

func writeProcs(path string, pid int) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.Write([]byte(strconv.Itoa(pid)))
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("split: write %s: %w", path, werr)
	}
	if cerr != nil {
		return fmt.Errorf("split: close %s: %w", path, cerr)
	}
	return nil
}

func saveState(s state) error {
	if err := os.MkdirAll(filepath.Dir(stateFile), 0700); err != nil {
		return fmt.Errorf("split: mkdir %s: %w", filepath.Dir(stateFile), err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := stateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("split: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, stateFile); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func removeState() error {
	err := os.Remove(stateFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
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

func nftDel() error {
	var errs []error
	for _, fam := range []string{"ip", "ip6"} {
		err := run("nft", "delete", "table", fam, tableName)
		if err != nil && !notFound(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// installRoutes adds rules tolerating ones already there — from a dead
// tunnel or the session being replaced — so reconnecting never opens a
// window with the split set unrouted.
func installRoutes(c Config) error {
	mark := fmt.Sprintf("%#x", fwmark)
	tbl := strconv.Itoa(routeTable)
	for _, fam := range []string{"-4", "-6"} {
		// Specific routes (LAN, docker bridges) must win over the split
		// table, or replies to forwarded sources head back out the
		// default. Same construct as wg-quick.
		err := addRule(fam, "fwmark", mark, "lookup", "main",
			"suppress_prefixlength", "0", "priority", strconv.Itoa(mainPri))
		if err == nil {
			err = addRule(fam, "fwmark", mark, "lookup", tbl,
				"priority", strconv.Itoa(rulePri))
		}
		if err != nil {
			return fmt.Errorf("split: ip %s rule add: %w", fam, err)
		}
	}
	if c.Split {
		return installTunnelRoutes(c, tbl)
	}
	return installPlainRoutes(c, tbl)
}

func addRule(fam string, args ...string) error {
	err := run("ip", append([]string{fam, "rule", "add"}, args...)...)
	if err != nil && strings.Contains(err.Error(), "File exists") {
		return nil
	}
	return err
}

// installTunnelRoutes points the marked table at the tunnel. The
// max-metric unreachable defaults outlive the wg interface, so tagged
// traffic fails closed instead of falling through to the plain default
// route if the tunnel disappears.
func installTunnelRoutes(c Config, tbl string) error {
	// The DNS rewrite targets must reach the tunnel even when a
	// main-table route covers them (a corporate 10/8, say), so they
	// get rules ahead of the suppress rule.
	for _, dns := range []netip.Addr{firstV4(c.DNS), firstV6(c.DNS)} {
		if !dns.IsValid() {
			continue
		}
		fam, bits := "-4", 32
		if dns.Is6() {
			fam, bits = "-6", 128
		}
		err := addRule(fam, "fwmark", fmt.Sprintf("%#x", fwmark),
			"to", fmt.Sprintf("%s/%d", dns, bits), "lookup", tbl, "priority", strconv.Itoa(dnsPri))
		if err != nil {
			return fmt.Errorf("split: ip %s rule add: %w", fam, err)
		}
	}
	for _, fam := range []string{"-4", "-6"} {
		if err := run("ip", fam, "route", "replace", "unreachable", "default", "metric", "4294967295", "table", tbl); err != nil {
			return fmt.Errorf("split: ip %s route: %w", fam, err)
		}
	}
	if err := run("ip", "-4", "route", "replace", "default", "dev", c.Iface, "table", tbl); err != nil {
		return fmt.Errorf("split: ip -4 route: %w", err)
	}
	if c.HasV6 {
		if err := run("ip", "-6", "route", "replace", "default", "dev", c.Iface, "table", tbl); err != nil {
			return fmt.Errorf("split: ip -6 route: %w", err)
		}
	}
	return nil
}

func installPlainRoutes(c Config, tbl string) error {
	// A dead split session may have left its DNS rules behind; inert in
	// this mode, but confusing in ip rule output.
	for _, fam := range []string{"-4", "-6"} {
		err := run("ip", fam, "rule", "del", "fwmark", fmt.Sprintf("%#x", fwmark),
			"lookup", tbl, "priority", strconv.Itoa(dnsPri))
		if err != nil && !notFound(err) {
			return err
		}
	}
	if err := run("ip", "-4", "route", "replace", "default", "via", c.Gateway.String(), "dev", c.Dev, "table", tbl); err != nil {
		return fmt.Errorf("split: ip -4 route replace: %w", err)
	}
	// Without a v6 default in table 60, marked v6 packets fall through
	// to the main table (the tunnel). Install an unreachable route so
	// the lookup terminates and apps fall back to v4.
	var args []string
	if c.Gateway6.IsValid() {
		args = []string{"-6", "route", "replace", "default", "via", c.Gateway6.String(), "dev", c.Dev, "table", tbl}
	} else {
		args = []string{"-6", "route", "replace", "unreachable", "default", "table", tbl}
	}
	if err := run("ip", args...); err != nil {
		return fmt.Errorf("split: ip -6 route: %w", err)
	}
	return nil
}

func delRule() error {
	mark := fmt.Sprintf("%#x", fwmark)
	tbl := strconv.Itoa(routeTable)
	var errs []error
	for _, fam := range []string{"-4", "-6"} {
		err := run("ip", fam, "rule", "del", "fwmark", mark, "lookup", tbl,
			"priority", strconv.Itoa(dnsPri))
		if err != nil && !notFound(err) {
			errs = append(errs, err)
		}
	}
	for _, fam := range []string{"-4", "-6"} {
		err := run("ip", fam, "rule", "del", "fwmark", mark, "lookup", "main",
			"suppress_prefixlength", "0", "priority", strconv.Itoa(mainPri))
		if err != nil && !notFound(err) {
			errs = append(errs, err)
		}
		err = run("ip", fam, "rule", "del", "fwmark", mark, "lookup", tbl,
			"priority", strconv.Itoa(rulePri))
		if err != nil && !notFound(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func delRoute() error {
	var errs []error
	for _, fam := range []string{"-4", "-6"} {
		err := run("ip", fam, "route", "flush", "table", strconv.Itoa(routeTable))
		if err != nil && !notFound(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func notFound(err error) bool {
	s := err.Error()
	return strings.Contains(s, "No such") ||
		strings.Contains(s, "Cannot find") ||
		strings.Contains(s, "does not exist")
}

func buildScript(c Config) string {
	c.Nets = coalesce(c.Nets)
	var b strings.Builder
	// Separate ip and ip6 tables because nftables nat chains aren't
	// supported in the inet family.
	for _, fam := range []string{"ip", "ip6"} {
		v6 := fam == "ip6"
		fmt.Fprintf(&b, "add table %s %s\n", fam, tableName)
		fmt.Fprintf(&b, "delete table %s %s\n", fam, tableName)
		fmt.Fprintf(&b, "table %s %s {\n", fam, tableName)
		writeNetSet(&b, v6, c.Nets)
		if c.Split {
			writeTunnelChains(&b, fam, v6, c)
		} else {
			writePlainChains(&b, fam, v6, c)
		}
		b.WriteString("}\n")
	}
	return b.String()
}

func writeNetSet(b *strings.Builder, v6 bool, nets []netip.Prefix) {
	typ := "ipv4_addr"
	if v6 {
		typ = "ipv6_addr"
	}
	fmt.Fprintf(b, "\tset %s {\n\t\ttype %s\n\t\tflags interval\n", setName, typ)
	var elems []string
	for _, p := range nets {
		if p.Addr().Is6() != v6 {
			continue
		}
		elems = append(elems, p.String())
	}
	if len(elems) > 0 {
		fmt.Fprintf(b, "\t\telements = { %s }\n", strings.Join(elems, ", "))
	}
	b.WriteString("\t}\n")
}

// writePlainChains tags traffic to escape the tunnel.
func writePlainChains(b *strings.Builder, fam string, v6 bool, c Config) {
	b.WriteString("\tchain output {\n")
	b.WriteString("\t\ttype route hook output priority -150;\n")
	writeDNSReturns(b, fam, v6, c.DNS)
	fmt.Fprintf(b, "\t\tsocket cgroupv2 level 1 %q meta mark set %#x\n", cgroupName, fwmark)
	b.WriteString("\t}\n")
	b.WriteString("\tchain prerouting {\n")
	b.WriteString("\t\ttype filter hook prerouting priority -150;\n")
	writeDNSReturns(b, fam, v6, c.DNS)
	// Replies from tagged sources need the mark too: the main-table
	// default is the tunnel here, and only the mark routes them back
	// out the physical interface.
	fmt.Fprintf(b, "\t\tct mark %#x meta mark set %#x return\n", fwmark, fwmark)
	fmt.Fprintf(b, "\t\t%s saddr @%s meta mark set %#x\n", fam, setName, fwmark)
	fmt.Fprintf(b, "\t\tmeta mark %#x ct mark set %#x\n", fwmark, fwmark)
	b.WriteString("\t}\n")
	// Another daemon rewriting resolv.conf (tailscaled) must not kill
	// resolution: queries not aimed at loopback, a tagged resolver, or
	// (with allow-lan) the LAN are rewritten to a Mullvad resolver.
	// Tagged traffic keeps the resolver it chose, on the plain path.
	dns, loop := firstV4(c.DNS), "127.0.0.0/8"
	if v6 {
		dns, loop = firstV6(c.DNS), "::1"
	}
	if dns.IsValid() {
		for _, chain := range [][2]string{
			{"dnsout", "type nat hook output priority -100;"},
			{"dnspre", "type nat hook prerouting priority dstnat;"},
		} {
			fmt.Fprintf(b, "\tchain %s {\n", chain[0])
			fmt.Fprintf(b, "\t\t%s\n", chain[1])
			fmt.Fprintf(b, "\t\t%s daddr @%s accept\n", fam, setName)
			if chain[0] == "dnsout" {
				fmt.Fprintf(b, "\t\t%s daddr %s accept\n", fam, loop)
			}
			fmt.Fprintf(b, "\t\tmeta mark %#x accept\n", fwmark)
			if c.AllowLAN {
				if v6 {
					b.WriteString("\t\tip6 daddr { fe80::/10, fc00::/7 } accept\n")
				} else {
					b.WriteString("\t\tip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16 } accept\n")
				}
			}
			fmt.Fprintf(b, "\t\tudp dport 53 dnat to %s\n", dns)
			fmt.Fprintf(b, "\t\ttcp dport 53 dnat to %s\n", dns)
			b.WriteString("\t}\n")
		}
	}
	// Marked packets keep the wg interface's source IP after re-routing,
	// so replies can't return. Masquerade to the physical interface.
	b.WriteString("\tchain postrouting {\n")
	b.WriteString("\t\ttype nat hook postrouting priority srcnat;\n")
	fmt.Fprintf(b, "\t\tmeta mark %#x oifname %q masquerade\n", fwmark, c.Dev)
	b.WriteString("\t}\n")
}

// writeTunnelChains tags traffic to ride the tunnel while the system
// default stays plain.
func writeTunnelChains(b *strings.Builder, fam string, v6 bool, c Config) {
	b.WriteString("\tchain output {\n")
	b.WriteString("\t\ttype route hook output priority -150;\n")
	b.WriteString("\t\tct direction reply return\n")
	fmt.Fprintf(b, "\t\tsocket cgroupv2 level 1 %q meta mark set %#x\n", cgroupName, fwmark)
	fmt.Fprintf(b, "\t\tmeta mark %#x ct mark set %#x\n", fwmark, fwmark)
	b.WriteString("\t}\n")
	b.WriteString("\tchain prerouting {\n")
	b.WriteString("\t\ttype filter hook prerouting priority -150;\n")
	fmt.Fprintf(b, "\t\tct mark %#x meta mark set %#x return\n", fwmark, fwmark)
	b.WriteString("\t\tct direction reply return\n")
	fmt.Fprintf(b, "\t\t%s saddr @%s meta mark set %#x\n", fam, setName, fwmark)
	fmt.Fprintf(b, "\t\tmeta mark %#x ct mark set %#x\n", fwmark, fwmark)
	b.WriteString("\t}\n")
	// Tagged traffic keeps the system resolver, which would answer in
	// the clear. Rewrite its DNS to a Mullvad resolver — except
	// queries to a tagged resolver, and to loopback stubs
	// (systemd-resolved): a loopback source cannot leave through the
	// tunnel, so those resolve via the host instead.
	dns, loop := firstV4(c.DNS), "127.0.0.0/8"
	if v6 {
		dns, loop = firstV6(c.DNS), "::1"
	}
	if dns.IsValid() {
		b.WriteString("\tchain dnsout {\n")
		b.WriteString("\t\ttype nat hook output priority -100;\n")
		fmt.Fprintf(b, "\t\t%s daddr @%s accept\n", fam, setName)
		fmt.Fprintf(b, "\t\t%s daddr %s accept\n", fam, loop)
		fmt.Fprintf(b, "\t\tmeta mark %#x udp dport 53 dnat to %s\n", fwmark, dns)
		fmt.Fprintf(b, "\t\tmeta mark %#x tcp dport 53 dnat to %s\n", fwmark, dns)
		b.WriteString("\t}\n")
		b.WriteString("\tchain dnspre {\n")
		b.WriteString("\t\ttype nat hook prerouting priority dstnat;\n")
		fmt.Fprintf(b, "\t\t%s daddr @%s accept\n", fam, setName)
		fmt.Fprintf(b, "\t\tmeta mark %#x udp dport 53 dnat to %s\n", fwmark, dns)
		fmt.Fprintf(b, "\t\tmeta mark %#x tcp dport 53 dnat to %s\n", fwmark, dns)
		b.WriteString("\t}\n")
	}
	// The suppress rule lets specific main-table routes win — how the
	// LAN and docker nets stay direct, but also how an injected route
	// could capture tunnel-bound traffic. Drop marked packets leaving
	// anywhere else.
	for _, chain := range [][2]string{{"guard", "output"}, {"guardfwd", "forward"}} {
		fmt.Fprintf(b, "\tchain %s {\n", chain[0])
		fmt.Fprintf(b, "\t\ttype filter hook %s priority 0;\n", chain[1])
		if chain[1] == "output" {
			b.WriteString("\t\toifname \"lo\" accept\n")
		}
		fmt.Fprintf(b, "\t\toifname %q accept\n", c.Iface)
		fmt.Fprintf(b, "\t\t%s daddr @%s accept\n", fam, setName)
		if v6 {
			b.WriteString("\t\tip6 daddr { fe80::/10, fc00::/7, ff02::/16 } accept\n")
		} else {
			b.WriteString("\t\tip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, 224.0.0.0/4 } accept\n")
		}
		fmt.Fprintf(b, "\t\tmeta mark %#x drop\n", fwmark)
		b.WriteString("\t}\n")
	}
	b.WriteString("\tchain postrouting {\n")
	b.WriteString("\t\ttype nat hook postrouting priority srcnat;\n")
	fmt.Fprintf(b, "\t\tmeta mark %#x oifname %q masquerade\n", fwmark, c.Iface)
	b.WriteString("\t}\n")
}

func writeDNSReturns(b *strings.Builder, fam string, v6 bool, dns []netip.Addr) {
	for _, a := range dns {
		if a.Is6() != v6 {
			continue
		}
		fmt.Fprintf(b, "\t\t%s daddr %s return\n", fam, a)
	}
}

func firstV4(dns []netip.Addr) netip.Addr {
	for _, a := range dns {
		if a.Is4() {
			return a
		}
	}
	return netip.Addr{}
}

func firstV6(dns []netip.Addr) netip.Addr {
	for _, a := range dns {
		if a.Is6() {
			return a
		}
	}
	return netip.Addr{}
}
