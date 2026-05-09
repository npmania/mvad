//go:build linux

package route

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	backup4 = "route4.bak"
	backup6 = "route6.bak"
)

func backupDir() string {
	d := os.Getenv("XDG_RUNTIME_DIR")
	if d == "" {
		d = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	return filepath.Join(d, "mvad")
}

func set(iface string) error {
	dir := backupDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := save("-4", filepath.Join(dir, backup4)); err != nil {
		return err
	}
	if err := save("-6", filepath.Join(dir, backup6)); err != nil {
		return err
	}
	if err := ip("-4", "route", "replace", "default", "dev", iface); err != nil {
		return err
	}
	return ip("-6", "route", "replace", "default", "dev", iface)
}

func unset() error {
	dir := backupDir()
	p4 := filepath.Join(dir, backup4)
	p6 := filepath.Join(dir, backup6)
	err4 := restore("-4", p4)
	if err4 == nil {
		os.Remove(p4)
	}
	err6 := restore("-6", p6)
	if err6 == nil {
		os.Remove(p6)
	}
	return errors.Join(err4, err6)
}

func save(family, path string) error {
	out, err := exec.Command("ip", family, "route", "show", "default").Output()
	if err != nil {
		return fmt.Errorf("ip %s route show default: %w", family, err)
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	f, err := os.CreateTemp(dir, "."+base+"-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := f.Chmod(0600); err != nil {
		f.Close()
		os.Remove(name)
		return err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		os.Remove(name)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

func restore(family, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	exec.Command("ip", family, "route", "del", "default").Run()
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		args := append([]string{family, "route", "add"}, strings.Fields(line)...)
		if err := ip(args...); err != nil {
			return err
		}
	}
	return nil
}

func ip(args ...string) error {
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}
