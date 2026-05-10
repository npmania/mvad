// Package config persists mvad on-disk state at
// $XDG_CONFIG_HOME/mvad/config.json (mode 0600).
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
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
	LastEndpoint      netip.AddrPort  `json:"last_endpoint,omitzero"`
	LastTransport     string          `json:"last_transport,omitempty"`
	LastTransportPort uint16          `json:"last_transport_port,omitempty"`
	LastBridge        string          `json:"last_bridge,omitempty"`
	RelayCache        json.RawMessage `json:"relay_cache,omitempty"`
	RelaysFetchedAt   time.Time       `json:"relays_fetched_at,omitzero"`
	AccountExpiry     time.Time       `json:"account_expiry,omitzero"`
	DeviceName        string          `json:"device_name,omitempty"`
	AllowLAN          bool            `json:"allow_lan,omitempty"`
	LockdownOn        bool            `json:"lockdown_on,omitempty"`
}

func path() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "mvad", "config.json"), nil
	}
	su, err := ResolveSudoUser()
	if err != nil {
		return "", err
	}
	home := ""
	if su != nil {
		home = su.Home
	} else {
		home, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(home, ".config", "mvad", "config.json"), nil
}

type SudoUser struct {
	Home     string
	UID, GID int
}

func ResolveSudoUser() (*SudoUser, error) {
	if os.Geteuid() != 0 {
		return nil, nil
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
	return &SudoUser{Home: u.HomeDir, UID: uid, GID: gid}, nil
}

func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	c := new(Config)
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	return c, nil
}

func mkdirChown(dir string, mode os.FileMode, su *SudoUser) error {
	var toMake []string
	cur := dir
	for {
		if _, err := os.Stat(cur); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		toMake = append(toMake, cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	for i := len(toMake) - 1; i >= 0; i-- {
		if err := os.Mkdir(toMake[i], mode); err != nil {
			return err
		}
		if su != nil {
			if err := os.Chown(toMake[i], su.UID, su.GID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Config) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	su, err := ResolveSudoUser()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := mkdirChown(dir, 0700, su); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "\t")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if su != nil {
		if err := os.Chown(name, su.UID, su.GID); err != nil {
			os.Remove(name)
			return err
		}
	}
	return os.Rename(name, p)
}
