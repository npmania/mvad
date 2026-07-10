//go:build linux

package main

import (
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
		return fmt.Errorf("split-tunnel cgroup %s missing; run mvad connect (or connect --split) first", split.CgroupDir)
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
	case "add-k8s":
		return splitAddK8s(rest)
	case "rm-k8s":
		return splitRmK8s(rest)
	case "refresh":
		return splitRefresh(rest)
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
	for _, s := range cfg.SplitDocker {
		fmt.Println("docker:" + s)
	}
	for _, s := range cfg.SplitCompose {
		fmt.Println("compose:" + s)
	}
	for _, s := range cfg.SplitK8s {
		fmt.Println("k8s:" + s)
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
	if cfg.SplitNets != nil || cfg.SplitDocker != nil || cfg.SplitCompose != nil || cfg.SplitK8s != nil {
		cfg.SplitNets = nil
		cfg.SplitDocker = nil
		cfg.SplitCompose = nil
		cfg.SplitK8s = nil
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
	// The source-address set only sees forwarded traffic; a host
	// address here would silently do nothing.
	if p.IsSingleIP() && isLocalAddr(p.Addr()) {
		return usagef("%s is a local address; select local processes with mvad run or split add-pid", p.Addr())
	}
	return splitAddEntry(splitNetsOf, p.String())
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
	return splitRmEntry(splitNetsOf, p.String())
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
	return splitAddEntry(splitDockerOf, args[0])
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
	return splitRmEntry(splitDockerOf, args[0])
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
	entry := joinEntry(args)
	project, service, _ := strings.Cut(entry, "/")
	ps, err := composeNets(project, service)
	if err != nil {
		return err
	}
	printNets(ps)
	return splitAddEntry(splitComposeOf, entry)
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
	// A bare project also removes its per-service entries.
	entry := joinEntry(args)
	return splitRmMatch(splitComposeOf, entry, func(s string) bool {
		return s == entry || strings.HasPrefix(s, entry+"/")
	})
}

func splitRefresh(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitRefresh)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 0 {
		return usagef(usageSplitRefresh)
	}
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
	if !split.Available() {
		return errors.New("split-tunnel inactive; run mvad connect first")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	return syncNets(cfg)
}

func joinEntry(args []string) string {
	if len(args) == 2 {
		return args[0] + "/" + args[1]
	}
	return args[0]
}

func splitAddK8s(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitAddK8s)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 1 && len(args) != 2 {
		return usagef(usageSplitAddK8s)
	}
	entry := joinEntry(args)
	ps, err := k8sNets(entry)
	if err != nil {
		return err
	}
	printNets(ps)
	return splitAddEntry(splitK8sOf, entry)
}

func splitRmK8s(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSplitRmK8s)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 1 && len(args) != 2 {
		return usagef(usageSplitRmK8s)
	}
	entry := joinEntry(args)
	// Exact only: selectors can contain "/", so the namespace prefix
	// rule below would swallow entries the user didn't name.
	if len(args) == 2 {
		return splitRmEntry(splitK8sOf, entry)
	}
	// A bare namespace also removes its per-pod entries.
	return splitRmMatch(splitK8sOf, entry, func(s string) bool {
		return s == entry || strings.HasPrefix(s, entry+"/")
	})
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
	// Unmap 4-in-6 input, or it lands in the v6 set and matches nothing.
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func splitNetsOf(c *config.Config) *[]string    { return &c.SplitNets }
func splitDockerOf(c *config.Config) *[]string  { return &c.SplitDocker }
func splitComposeOf(c *config.Config) *[]string { return &c.SplitCompose }
func splitK8sOf(c *config.Config) *[]string     { return &c.SplitK8s }

// splitAddEntry records the entry in config, so connect reseeds it,
// and reconciles the live set when one is installed.
func splitAddEntry(list func(*config.Config) *[]string, entry string) error {
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	l := list(cfg)
	if slices.Contains(*l, entry) {
		return nil
	}
	*l = append(*l, entry)
	if err := cfg.Save(); err != nil {
		return err
	}
	return syncNets(cfg)
}

func splitRmEntry(list func(*config.Config) *[]string, entry string) error {
	return splitRmMatch(list, entry, func(s string) bool { return s == entry })
}

func splitRmMatch(list func(*config.Config) *[]string, entry string, match func(string) bool) error {
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	l := list(cfg)
	n := len(*l)
	*l = slices.DeleteFunc(*l, match)
	if len(*l) == n {
		return fmt.Errorf("%s not in the split set (see mvad split list)", entry)
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	return syncNets(cfg)
}

// syncNets reconciles the live set with config. When an entry doesn't
// resolve it refuses to reconcile: flushing addresses on a docker
// hiccup would strip running containers out of the tunnel.
func syncNets(cfg *config.Config) error {
	if !split.Available() {
		return nil
	}
	nets, errs := resolveSplitNets(cfg)
	if len(errs) != 0 {
		return fmt.Errorf("%w; live set unchanged", errors.Join(errs...))
	}
	return split.SetNets(nets)
}
