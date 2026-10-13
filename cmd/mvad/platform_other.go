//go:build !linux && !darwin

package main

import (
	"errors"
	"os/exec"
)

const (
	runDir       = "/run/mvad"
	wgHint       = " (is the wireguard kernel module loaded?)"
	firewallFail = "install nft kill-switch: %w (run nft list ruleset as root to inspect)"
)

func udp2tcpShimAlive(int) bool { return false }
func ssAlive(int) bool          { return false }

func wgShim([]string) error { return usagef("unknown command %q", "__wg") }

func ssLocal() (string, error) { return exec.LookPath("ss-local") }

func up([]string) error { return errors.New("up: unsupported platform") }
