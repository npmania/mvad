package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func listUserProcs(uid int) []splitPID {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []splitPID
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if procUID(pid) != uid {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(bytes.Trim(cmd, "\x00")) == 0 {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		out = append(out, splitPID{pid: pid, comm: strings.TrimSpace(string(comm))})
	}
	return out
}

func procUID(pid int) int  { return procStatusInt(pid, "Uid:") }
func procPPID(pid int) int { return procStatusInt(pid, "PPid:") }

func procStatusInt(pid int, prefix string) int {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		f := strings.Fields(line[len(prefix):])
		if len(f) == 0 {
			return -1
		}
		n, err := strconv.Atoi(f[0])
		if err != nil {
			return -1
		}
		return n
	}
	return -1
}
