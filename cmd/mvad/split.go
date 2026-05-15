//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"

	"github.com/npmania/mvad/internal/config"
	"github.com/npmania/mvad/internal/split"
)

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
	if err := split.AddPID(os.Getpid()); err != nil {
		return err
	}
	if err := dropPrivs(); err != nil {
		return err
	}
	return syscall.Exec(bin, args, os.Environ())
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
	if err != nil {
		return err
	}
	for _, pid := range pids {
		fmt.Println(pid)
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
	return split.Clear()
}
