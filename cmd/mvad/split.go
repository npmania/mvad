//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/npmania/mvad/internal/config"
	"github.com/npmania/mvad/internal/lock"
	"github.com/npmania/mvad/internal/split"
)

// escapeSplitCgroup moves this process out of the split cgroup so
// children (the transport shims) don't inherit the tag when connect is
// run from an mvad run shell.
func escapeSplitCgroup() {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return
	}
	in := false
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		path, ok := strings.CutPrefix(line, "0::")
		if ok && (path == "/mvad-split" || strings.HasPrefix(path, "/mvad-split/")) {
			in = true
		}
	}
	if !in {
		return
	}
	f, err := os.OpenFile("/sys/fs/cgroup/cgroup.procs", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	_, werr := fmt.Fprintf(f, "%d", os.Getpid())
	if cerr := f.Close(); werr != nil || cerr != nil {
		fmt.Fprintln(os.Stderr, "mvad: leaving split cgroup failed; transport shims stay tagged")
	}
}

func runCmd(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--h", "-help", "--help":
			fmt.Println(usageRun)
			return nil
		}
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return usagef(usageRun)
	}
	if !split.Available() {
		return fmt.Errorf("split-tunnel cgroup %s missing; run mvad connect first", split.CgroupDir)
	}
	bin, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	// pkexec scrubs DISPLAY, DBUS_SESSION_BUS_ADDRESS, etc.; restore them
	// from the invoking process so the child reaches the user's session.
	var invokerEnv []string
	if os.Getenv("PKEXEC_UID") != "" {
		invokerEnv = readInvokerEnv()
	}
	if err := split.AddPID(os.Getpid()); err != nil {
		return err
	}
	if err := dropPrivs(); err != nil {
		return err
	}
	env := os.Environ()
	if invokerEnv != nil {
		env = invokerEnv
	}
	return syscall.Exec(bin, args, env)
}

func readInvokerEnv() []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", os.Getppid()))
	if err != nil {
		return nil
	}
	var env []string
	for p := range bytes.SplitSeq(data, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		env = append(env, string(p))
	}
	return env
}

// dropPrivs drops to the user who invoked mvad via sudo or pkexec.
// A no-op when mvad was launched directly as root.
func dropPrivs() error {
	cu, err := config.ResolveCallingUser()
	if err != nil || cu == nil {
		return err
	}
	u, err := user.LookupId(strconv.Itoa(cu.UID))
	if err != nil {
		return err
	}
	ids, err := u.GroupIds()
	if err != nil {
		return err
	}
	gids := make([]int, len(ids))
	for i, s := range ids {
		n, err := strconv.Atoi(s)
		if err != nil {
			return err
		}
		gids[i] = n
	}
	if err := syscall.Setgroups(gids); err != nil {
		return err
	}
	if err := syscall.Setgid(cu.GID); err != nil {
		return err
	}
	if err := syscall.Setuid(cu.UID); err != nil {
		return err
	}
	for _, k := range []string{"SUDO_USER", "SUDO_UID", "SUDO_GID", "SUDO_COMMAND", "PKEXEC_UID"} {
		os.Unsetenv(k)
	}
	os.Setenv("HOME", cu.Home)
	os.Setenv("USER", u.Username)
	os.Setenv("LOGNAME", u.Username)
	return nil
}

func splitCmd(args []string) error {
	if len(args) == 0 {
		return usagef(usageSplit)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "--h", "-help", "--help":
		fmt.Println(usageSplit)
		return nil
	case "add-pid":
		return splitAddPID(rest)
	case "rm-pid":
		return splitRmPID(rest)
	case "add-ip":
		return splitAddIP(rest)
	case "rm-ip":
		return splitRmIP(rest)
	case "add-docker":
		return splitAddDocker(rest)
	case "rm-docker":
		return splitRmDocker(rest)
	case "add-compose":
		return splitAddCompose(rest)
	case "rm-compose":
		return splitRmCompose(rest)
	case "list":
		return splitList(rest)
	case "clear":
		return splitClear(rest)
	default:
		return usagef("unknown split subcommand %q", sub)
	}
}

func splitAddPID(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitAddPID)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 1 {
		return usagef(usageSplitAddPID)
	}
	pid, err := strconv.Atoi(args[0])
	if err != nil || pid <= 0 {
		return usagef("invalid pid %q", args[0])
	}
	return split.AddPID(pid)
}

func splitRmPID(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitRmPID)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 1 {
		return usagef(usageSplitRmPID)
	}
	pid, err := strconv.Atoi(args[0])
	if err != nil || pid <= 0 {
		return usagef("invalid pid %q", args[0])
	}
	return split.RmPID(pid)
}

func splitList(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitList)
		return nil
	}
	if len(args) != 0 {
		return usagef(usageSplitList)
	}
	pids, err := split.ListPIDs()
	if err != nil && !errors.Is(err, split.ErrUnavailable) {
		return err
	}
	for _, pid := range pids {
		fmt.Println(pid)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, s := range cfg.SplitNets {
		fmt.Println(s)
	}
	return nil
}

func splitClear(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitClear)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 0 {
		return usagef(usageSplitClear)
	}
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
	clearErr := split.Clear()
	cfg, err := config.Load()
	if err != nil {
		return errors.Join(clearErr, err)
	}
	var saveErr error
	if cfg.SplitNets != nil {
		cfg.SplitNets = nil
		saveErr = cfg.Save()
	}
	return errors.Join(clearErr, saveErr)
}

func splitAddIP(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitAddIP)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 1 {
		return usagef(usageSplitAddIP)
	}
	p, err := parseNet(args[0])
	if err != nil {
		return err
	}
	return addNets([]netip.Prefix{p})
}

func splitRmIP(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitRmIP)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 1 {
		return usagef(usageSplitRmIP)
	}
	p, err := parseNet(args[0])
	if err != nil {
		return err
	}
	return rmNets([]netip.Prefix{p})
}

func splitAddDocker(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitAddDocker)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 1 {
		return usagef(usageSplitAddDocker)
	}
	ps, err := dockerNets(args[0])
	if err != nil {
		return err
	}
	if len(ps) == 0 {
		return fmt.Errorf("container %s has no IP address; is it running?", args[0])
	}
	printNets(ps)
	return addNets(ps)
}

func splitRmDocker(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitRmDocker)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 1 {
		return usagef(usageSplitRmDocker)
	}
	ps, err := dockerNets(args[0])
	if err != nil {
		return err
	}
	if len(ps) == 0 {
		return fmt.Errorf("container %s has no IP address; is it running?", args[0])
	}
	printNets(ps)
	return rmNets(ps)
}

func splitAddCompose(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitAddCompose)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 1 && len(args) != 2 {
		return usagef(usageSplitAddCompose)
	}
	ps, err := composeNets(args[0], composeService(args))
	if err != nil {
		return err
	}
	printNets(ps)
	return addNets(ps)
}

func splitRmCompose(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitRmCompose)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 1 && len(args) != 2 {
		return usagef(usageSplitRmCompose)
	}
	ps, err := composeNets(args[0], composeService(args))
	if err != nil {
		return err
	}
	printNets(ps)
	return rmNets(ps)
}

func composeService(args []string) string {
	if len(args) == 2 {
		return args[1]
	}
	return ""
}

func parseNet(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		if p != p.Masked() {
			return netip.Prefix{}, usagef("%s has host bits set; use %s", s, p.Masked())
		}
		return p, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, usagef("invalid address %q", s)
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func printNets(ps []netip.Prefix) {
	for _, p := range ps {
		fmt.Println(p)
	}
}

// addNets records the prefixes in config, so connect reseeds them, and
// updates the live nft set when one is installed.
func addNets(ps []netip.Prefix) error {
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// An overlapping element poisons the seeded set on the next
	// connect (nft rejects conflicting intervals), so refuse it here.
	have := parseNets(cfg.SplitNets)
	changed := false
	for _, p := range ps {
		if slices.Contains(cfg.SplitNets, p.String()) {
			continue
		}
		for _, q := range have {
			if p.Overlaps(q) {
				return fmt.Errorf("%s overlaps %s; rm-ip it first", p, q)
			}
		}
		have = append(have, p)
		cfg.SplitNets = append(cfg.SplitNets, p.String())
		changed = true
	}
	if changed {
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	if !split.Available() {
		return nil
	}
	for _, p := range ps {
		if err := split.AddNet(p); err != nil {
			return err
		}
	}
	return nil
}

func rmNets(ps []netip.Prefix) error {
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	n := len(cfg.SplitNets)
	for _, p := range ps {
		cfg.SplitNets = slices.DeleteFunc(cfg.SplitNets, func(s string) bool { return s == p.String() })
	}
	if len(cfg.SplitNets) != n {
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	if !split.Available() {
		return nil
	}
	for _, p := range ps {
		if err := split.RmNet(p); err != nil {
			return err
		}
	}
	return nil
}

func dockerNets(container string) ([]netip.Prefix, error) {
	out, err := dockerOutput("inspect", "--type", "container", "--format",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}} {{.GlobalIPv6Address}} {{end}}", container)
	if err != nil {
		return nil, err
	}
	var ps []netip.Prefix
	for _, f := range strings.Fields(string(out)) {
		a, err := netip.ParseAddr(f)
		if err != nil {
			continue
		}
		ps = append(ps, netip.PrefixFrom(a, a.BitLen()))
	}
	return ps, nil
}

func composeNets(project, service string) ([]netip.Prefix, error) {
	args := []string{"ps", "-q", "--no-trunc", "--filter", "label=com.docker.compose.project=" + project}
	if service != "" {
		args = append(args, "--filter", "label=com.docker.compose.service="+service)
	}
	out, err := dockerOutput(args...)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	var ps []netip.Prefix
	for _, id := range ids {
		got, err := dockerNets(id)
		if err != nil {
			return nil, err
		}
		ps = append(ps, got...)
	}
	if len(ps) == 0 {
		what := "project " + project
		if service != "" {
			what = "service " + project + "/" + service
		}
		return nil, fmt.Errorf("no addressed running containers for compose %s", what)
	}
	return ps, nil
}

func dockerOutput(args ...string) ([]byte, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil, errors.New("docker not found in PATH")
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}
