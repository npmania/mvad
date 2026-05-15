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
	Gateway netip.Addr `json:"gateway"`
	Dev     string     `json:"dev"`
}

func up(gw netip.Addr, dev string, viaTunnel []netip.Addr) error {
	if !cgroupV2Mounted() {
		return errors.New("split: cgroup v2 unified hierarchy not mounted at /sys/fs/cgroup")
	}
	if !gw.IsValid() {
		return errors.New("split: invalid gateway")
	}
	if dev == "" {
		return errors.New("split: empty device")
	}
	if err := os.MkdirAll(cgroupDir, 0755); err != nil {
		return fmt.Errorf("split: mkdir %s: %w", cgroupDir, err)
	}
	if err := saveState(state{Gateway: gw, Dev: dev}); err != nil {
		return err
	}
	if err := runNft(buildScript(viaTunnel, dev)); err != nil {
		_ = removeState()
		return fmt.Errorf("split: install nft: %w", err)
	}
	if err := installRoutes(gw, dev); err != nil {
		_ = nftDel()
		_ = removeState()
		return err
	}
	return nil
}

func down() error {
	if _, err := os.Stat(stateFile); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return errors.Join(delRule(), delRoute(), nftDel(), removeState())
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

func clear() error {
	if !cgroupExists() {
		return nil
	}
	pids, err := listPIDs()
	if err != nil {
		return err
	}
	root := filepath.Join(cgroupRoot, "cgroup.procs")
	var errs []error
	for _, pid := range pids {
		if err := writeProcs(root, pid); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			errs = append(errs, fmt.Errorf("split: move pid %d: %w", pid, err))
		}
	}
	return errors.Join(errs...)
}

func available() bool {
	_, err := os.Stat(stateFile)
	return err == nil
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
	err := run("nft", "delete", "table", "ip", tableName)
	if err == nil || notFound(err) {
		return nil
	}
	return err
}

func installRoutes(gw netip.Addr, dev string) error {
	mark := fmt.Sprintf("%#x", fwmark)
	tbl := strconv.Itoa(routeTable)
	pri := strconv.Itoa(rulePri)
	if err := run("ip", "rule", "add", "fwmark", mark, "lookup", tbl, "priority", pri); err != nil {
		return fmt.Errorf("split: ip rule add: %w", err)
	}
	if err := run("ip", "route", "replace", "default", "via", gw.String(), "dev", dev, "table", tbl); err != nil {
		_ = delRule()
		return fmt.Errorf("split: ip route replace: %w", err)
	}
	return nil
}

func delRule() error {
	mark := fmt.Sprintf("%#x", fwmark)
	tbl := strconv.Itoa(routeTable)
	pri := strconv.Itoa(rulePri)
	err := run("ip", "rule", "del", "fwmark", mark, "lookup", tbl, "priority", pri)
	if err == nil || notFound(err) {
		return nil
	}
	return err
}

func delRoute() error {
	err := run("ip", "route", "flush", "table", strconv.Itoa(routeTable))
	if err == nil || notFound(err) {
		return nil
	}
	return err
}

func notFound(err error) bool {
	s := err.Error()
	return strings.Contains(s, "No such") ||
		strings.Contains(s, "Cannot find") ||
		strings.Contains(s, "does not exist")
}

func buildScript(viaTunnel []netip.Addr, dev string) string {
	var b strings.Builder
	// ip family because nftables nat chains aren't supported in inet.
	// Split-tunnel only re-routes IPv4 anyway (no v6 fwmark rule).
	fmt.Fprintf(&b, "add table ip %s\n", tableName)
	fmt.Fprintf(&b, "delete table ip %s\n", tableName)
	fmt.Fprintf(&b, "table ip %s {\n", tableName)
	b.WriteString("\tchain output {\n")
	b.WriteString("\t\ttype route hook output priority -150;\n")
	for _, a := range viaTunnel {
		if !a.Is4() {
			continue
		}
		fmt.Fprintf(&b, "\t\tip daddr %s return\n", a)
	}
	fmt.Fprintf(&b, "\t\tsocket cgroupv2 level 1 %q meta mark set %#x\n", cgroupName, fwmark)
	b.WriteString("\t}\n")
	// Marked packets keep the wg interface's source IP after re-routing,
	// so replies can't return. Masquerade to the physical interface.
	b.WriteString("\tchain postrouting {\n")
	b.WriteString("\t\ttype nat hook postrouting priority srcnat;\n")
	fmt.Fprintf(&b, "\t\tmeta mark %#x oifname %q masquerade\n", fwmark, dev)
	b.WriteString("\t}\n")
	b.WriteString("}\n")
	return b.String()
}
