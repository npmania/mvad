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
	"path/filepath"
	"strconv"
	"syscall"
)

const (
	resolvConf = "/etc/resolv.conf"
	backupName = "resolv.conf.bak"
)

var errSymlink = errors.New("/etc/resolv.conf is a symlink; mvad does not support systemd-resolved or NetworkManager-managed resolvers")

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

func set(servers []netip.Addr) error {
	if len(servers) == 0 {
		return errors.New("dns: no servers")
	}
	fi, err := os.Lstat(resolvConf)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return errSymlink
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

func restore() error {
	bak := backupPath()
	data, err := os.ReadFile(bak)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("dns: no backup to restore")
		}
		return err
	}
	if fi, err := os.Lstat(resolvConf); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return errSymlink
	}
	if err := writeAtomic(resolvConf, data, 0644); err != nil {
		return err
	}
	return os.Remove(bak)
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
