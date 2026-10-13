// Package config persists mvad on-disk state at
// $XDG_CONFIG_HOME/mvad/config.json (mode 0600). Root acting for a
// calling user (sudo, pkexec) works only in directories that user owns.
package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type Config struct {
	AccountToken      string          `json:"account_token,omitempty"`
	DeviceID          string          `json:"device_id,omitempty"`
	PrivateKey        string          `json:"private_key,omitempty"`
	DeviceIPv4        netip.Prefix    `json:"device_ipv4,omitzero"`
	DeviceIPv6        netip.Prefix    `json:"device_ipv6,omitzero"`
	LastRelay         string          `json:"last_relay,omitempty"`
	LastEntryRelay    string          `json:"last_entry_relay,omitempty"`
	LastQuery         string          `json:"last_query,omitempty"`
	LastVia           string          `json:"last_via,omitempty"`
	LastEndpoint      netip.AddrPort  `json:"last_endpoint,omitzero"`
	LastTransport     string          `json:"last_transport,omitempty"`
	LastTransportPort uint16          `json:"last_transport_port,omitempty"`
	LastBridge        string          `json:"last_bridge,omitempty"`
	LastSplit         bool            `json:"last_split,omitempty"`
	LastAllowLAN      bool            `json:"last_allow_lan,omitempty"`
	RelayCache        json.RawMessage `json:"relay_cache,omitempty"`
	RelaysFetchedAt   time.Time       `json:"relays_fetched_at,omitzero"`
	AccountExpiry     time.Time       `json:"account_expiry,omitzero"`
	DeviceName        string          `json:"device_name,omitempty"`
	AllowLAN          bool            `json:"allow_lan,omitempty"`
	LockdownOn        bool            `json:"lockdown_on,omitempty"`
	NoCloseToTray     bool            `json:"no_close_to_tray,omitempty"`
	Dark              bool            `json:"dark,omitempty"`
	DarkSet           bool            `json:"dark_set,omitempty"`
	SplitOtherOpen    bool            `json:"split_other_open,omitempty"`
	Favorites         []string        `json:"favorites,omitempty"`
	SplitApps         []string        `json:"split_apps,omitempty"`
	SplitNets         []string        `json:"split_nets,omitempty"`
	SplitDocker       []string        `json:"split_docker,omitempty"`
	SplitCompose      []string        `json:"split_compose,omitempty"`
	SplitK8s          []string        `json:"split_k8s,omitempty"`
}

// location returns the config file's path and the calling user root
// acts for, if any.
func location() (string, *CallingUser, error) {
	cu, err := ResolveCallingUser()
	if err != nil {
		return "", nil, err
	}
	// The XDG spec has relative paths ignored.
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "mvad", "config.json"), cu, nil
	}
	home := ""
	if cu != nil {
		home = cu.Home
	} else {
		home, err = os.UserHomeDir()
		if err != nil {
			return "", nil, err
		}
	}
	return filepath.Join(home, ".config", "mvad", "config.json"), cu, nil
}

type CallingUser struct {
	Home     string
	UID, GID int
}

func ResolveCallingUser() (*CallingUser, error) {
	if os.Geteuid() != 0 {
		return nil, nil
	}
	if uid := os.Getenv("PKEXEC_UID"); uid != "" {
		n, err := strconv.Atoi(uid)
		if err != nil {
			return nil, err
		}
		u, err := user.LookupId(uid)
		if err != nil {
			return nil, err
		}
		gid, err := strconv.Atoi(u.Gid)
		if err != nil {
			return nil, err
		}
		return &CallingUser{Home: u.HomeDir, UID: n, GID: gid}, nil
	}
	name := os.Getenv("SUDO_USER")
	if name == "" {
		return nil, nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, err
	}
	return &CallingUser{Home: u.HomeDir, UID: uid, GID: gid}, nil
}

func Load() (*Config, error) {
	p, cu, err := location()
	if err != nil {
		return nil, err
	}
	data, err := readFile(p, cu)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", p, err)
	}
	c := new(Config)
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("config: %s: %w", p, err)
	}
	return c, nil
}

func readFile(p string, cu *CallingUser) ([]byte, error) {
	r, err := openDir(filepath.Dir(p), cu, false)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	// O_NONBLOCK: a FIFO in its place must not hang the open.
	f, err := r.OpenFile(filepath.Base(p), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if cu != nil && !ownedBy(fi, cu.UID) {
		return nil, fmt.Errorf("not owned by uid %d", cu.UID)
	}
	return io.ReadAll(f)
}

// openDir opens dir, creating it and missing parents (0700) if create
// is set. For a non-nil cu, created directories go to cu, and every
// directory opened must belong to cu, checked on the open handle.
func openDir(dir string, cu *CallingUser, create bool) (*os.Root, error) {
	// The trailing separator makes the kernel refuse anything but a
	// directory before opening it.
	r, err := os.OpenRoot(dir + string(filepath.Separator))
	if create && errors.Is(err, fs.ErrNotExist) && filepath.Dir(dir) != dir {
		r, err = mkdir(dir, cu)
	}
	if err != nil {
		return nil, err
	}
	fi, err := r.Stat(".")
	if err != nil {
		r.Close()
		return nil, err
	}
	if cu != nil && !ownedBy(fi, cu.UID) {
		r.Close()
		return nil, fmt.Errorf("%s not owned by uid %d", dir, cu.UID)
	}
	return r, nil
}

// mkdir creates dir through its opened parent and, for a non-nil cu,
// chowns it through its own handle: chowning by name could hit a hard
// link swapped in after the mkdir.
func mkdir(dir string, cu *CallingUser) (*os.Root, error) {
	parent, err := openDir(filepath.Dir(dir), cu, true)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := filepath.Base(dir)
	err = parent.Mkdir(name, 0700)
	made := err == nil
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	r, err := parent.OpenRoot(name + string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	if made && cu != nil {
		if err := r.Chown(".", cu.UID, cu.GID); err != nil {
			r.Close()
			parent.Remove(name)
			return nil, err
		}
	}
	return r, nil
}

func (c *Config) Save() error {
	p, cu, err := location()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "\t")
	if err != nil {
		return err
	}
	if err := writeFile(p, data, cu); err != nil {
		return fmt.Errorf("config: %s: %w", p, err)
	}
	return nil
}

// writeFile replaces p through a temporary file and a rename.
func writeFile(p string, data []byte, cu *CallingUser) error {
	r, err := openDir(filepath.Dir(p), cu, true)
	if err != nil {
		return err
	}
	defer r.Close()
	name := ".config-" + rand.Text() + ".json"
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		r.Remove(name)
		return err
	}
	if cu != nil {
		if err := f.Chown(cu.UID, cu.GID); err != nil {
			f.Close()
			r.Remove(name)
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		r.Remove(name)
		return err
	}
	if err := f.Close(); err != nil {
		r.Remove(name)
		return err
	}
	if err := r.Rename(name, filepath.Base(p)); err != nil {
		r.Remove(name)
		return err
	}
	return nil
}
