//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestProcArgs(t *testing.T) {
	if got := procArgs(os.Getpid()); !slices.Equal(got, os.Args) {
		t.Errorf("procArgs(self) = %q, want %q", got, os.Args)
	}
}

func TestProcName(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Base(exe)
	want = want[:min(len(want), 16)] // MAXCOMLEN
	if got := procName(os.Getpid()); got != want {
		t.Errorf("procName(self) = %q, want %q", got, want)
	}
}

func TestShimAlive(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "read x", "__udp2tcp")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if !udp2tcpShimAlive(pid) {
		t.Errorf("udp2tcpShimAlive(%d) = false for argv %q", pid, procArgs(pid))
	}
	if ssAlive(pid) {
		t.Errorf("ssAlive(%d) = true for %q", pid, procName(pid))
	}
	stdin.Close()
	cmd.Wait()
	if udp2tcpShimAlive(pid) {
		t.Errorf("udp2tcpShimAlive(%d) = true after exit", pid)
	}
	if udp2tcpShimAlive(os.Getpid()) {
		t.Error("udp2tcpShimAlive(self) = true")
	}
}
