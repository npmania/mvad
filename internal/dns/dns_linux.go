//go:build linux

package dns

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	resolvConf  = "/etc/resolv.conf"
	resolvedDir = "/run/systemd/resolve/"
	backupName  = "resolv.conf.bak"
)

var errSymlink = errors.New("/etc/resolv.conf is a symlink to an unsupported resolver manager")

func runtimeDir() string {
	d := os.Getenv("XDG_RUNTIME_DIR")
	if d == "" {
		d = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	return filepath.Join(d, "mvad")
}

func backupPath() string {
	return filepath.Join(runtimeDir(), backupName)
}

func usingResolved() (bool, error) {
	fi, err := os.Lstat(resolvConf)
	if err != nil {
		return false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t, err := os.Readlink(resolvConf)
		if err != nil {
			return false, err
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(resolvConf), t)
		}
		if strings.HasPrefix(filepath.Clean(t), resolvedDir) {
			return true, nil
		}
		return false, errSymlink
	}
	data, err := readNoFollow(resolvConf)
	if err != nil {
		return false, err
	}
	return firstNameserver(data) == "127.0.0.53", nil
}

func firstNameserver(data []byte) string {
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			return f[1]
		}
	}
	return ""
}

func set(iface string, servers []netip.Addr) error {
	if len(servers) == 0 {
		return errors.New("dns: no servers")
	}
	resolved, err := usingResolved()
	if err != nil {
		return err
	}
	if resolved {
		if _, err := exec.LookPath("resolvectl"); err != nil {
			return errors.New("systemd-resolved detected but resolvectl not found")
		}
		args := []string{"dns", iface}
		for _, s := range servers {
			args = append(args, s.String())
		}
		if err := resolvectl(args...); err != nil {
			return err
		}
		if err := resolvectl("domain", iface, "~."); err != nil {
			_ = resolvectlRevert(iface)
			return err
		}
		if err := resolvectl("default-route", iface, "yes"); err != nil {
			_ = resolvectlRevert(iface)
			return err
		}
		return nil
	}
	data, err := readNoFollow(resolvConf)
	if err != nil {
		return err
	}
	bak := backupPath()
	if err := os.MkdirAll(filepath.Dir(bak), 0700); err != nil {
		return err
	}
	if err := writeAtomic(bak, data, 0600); err != nil {
		return err
	}
	var b bytes.Buffer
	for _, s := range servers {
		fmt.Fprintf(&b, "nameserver %s\n", s)
	}
	return writeAtomic(resolvConf, b.Bytes(), 0644)
}

func restore(iface string) error {
	resolved, err := usingResolved()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if resolved {
		return resolvectlRevert(iface)
	}
	bak := backupPath()
	data, err := os.ReadFile(bak)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := writeAtomic(resolvConf, data, 0644); err != nil {
		return err
	}
	return os.Remove(bak)
}

func resolvectl(args ...string) error {
	cmd := exec.Command("resolvectl", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("resolvectl %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

func resolvectlRevert(iface string) error {
	cmd := exec.Command("resolvectl", "revert", iface)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	s := string(out)
	if strings.Contains(s, "not known") || strings.Contains(s, "No such") || strings.Contains(s, "not found") {
		return nil
	}
	return fmt.Errorf("resolvectl revert %s: %w: %s", iface, err, bytes.TrimSpace(out))
}

func readNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	f, err := os.CreateTemp(dir, "."+base+"-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := f.Chmod(mode); err != nil {
		f.Close()
		os.Remove(name)
		return err
	}
	if _, err := f.Write(data); err != nil {
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
