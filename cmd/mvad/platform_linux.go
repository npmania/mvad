//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	runDir       = "/run/mvad"
	wgHint       = " (is the wireguard kernel module loaded?)"
	firewallFail = "install nft kill-switch: %w (run nft list ruleset as root to inspect)"
)

// udp2tcpShimAlive reports whether pid still names our shim, guarding
// against PID recycling between connect and disconnect.
func udp2tcpShimAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "__udp2tcp")
}

// ssAlive reports whether pid still runs ss-local, guarding against
// PID recycling. /proc/<pid>/exe is kernel-set; cmdline is not.
func ssAlive(pid int) bool {
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return false
	}
	return filepath.Base(target) == "ss-local"
}

func wgShim([]string) error { return usagef("unknown command %q", "__wg") }

func ssLocal() (string, error) { return exec.LookPath("ss-local") }
