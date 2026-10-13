//go:build darwin

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/npmania/mvad/internal/wg"
)

const (
	runDir       = "/var/run/mvad"
	wgHint       = ""
	firewallFail = "install pf kill-switch: %w (run pfctl -a com.apple/mvad -sr as root to inspect)"
)

// udp2tcpShimAlive reports whether pid still names our shim, guarding
// against PID recycling between connect and disconnect.
func udp2tcpShimAlive(pid int) bool {
	return slices.Contains(procArgs(pid), "__udp2tcp")
}

// procArgs returns the argv of pid. kern.procargs2 holds argc, the
// exec path, NUL padding, then argv and the environment.
func procArgs(pid int) []string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return nil
	}
	argc := int(binary.NativeEndian.Uint32(b))
	_, b, _ = bytes.Cut(b[4:], []byte{0})
	b = bytes.TrimLeft(b, "\x00")
	var args []string
	for len(args) < argc {
		a, rest, ok := bytes.Cut(b, []byte{0})
		if !ok {
			break
		}
		args = append(args, string(a))
		b = rest
	}
	return args
}

// ssAlive reports whether pid still runs ss-local, guarding against
// PID recycling.
func ssAlive(pid int) bool { return procName(pid) == "ss-local" }

// procName returns p_comm, which the kernel sets from the file name
// of the executed image; argv[0] is the caller's choice.
func procName(pid int) string {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ""
	}
	c := k.Proc.P_comm[:]
	if i := bytes.IndexByte(c, 0); i >= 0 {
		c = c[:i]
	}
	return string(c)
}

func wgShim(args []string) error {
	if len(args) != 1 {
		return errors.New("internal subcommand")
	}
	return wg.Serve(args[0])
}

// ssLocal also looks where Homebrew installs: the GUI and launchd run
// mvad with a PATH that lacks it.
func ssLocal() (string, error) {
	if p, err := exec.LookPath("ss-local"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/ss-local", "/usr/local/bin/ss-local"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("ss-local not found")
}
