//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/npmania/mvad/internal/split"
)

func runCmd(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageRun)
		return nil
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
	return syscall.Exec(bin, args, os.Environ())
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
