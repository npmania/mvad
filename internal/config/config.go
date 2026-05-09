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
	"time"
)

type Config struct {
	AccountToken    string          `json:"account_token,omitempty"`
	DeviceID        string          `json:"device_id,omitempty"`
	PrivateKey      string          `json:"private_key,omitempty"`
	DeviceIPv4      netip.Prefix    `json:"device_ipv4,omitzero"`
	DeviceIPv6      netip.Prefix    `json:"device_ipv6,omitzero"`
	LastRelay       string          `json:"last_relay,omitempty"`
	RelayCache      json.RawMessage `json:"relay_cache,omitempty"`
	RelaysFetchedAt time.Time       `json:"relays_fetched_at,omitzero"`
}

func path() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "mvad", "config.json"), nil
	}
	var home string
	if os.Geteuid() == 0 {
		if name := os.Getenv("SUDO_USER"); name != "" {
			u, err := user.Lookup(name)
			if err != nil {
				return "", err
			}
			home = u.HomeDir
		}
	}
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = h
	}
	return filepath.Join(home, ".config", "mvad", "config.json"), nil
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

func (c *Config) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0700); err != nil {
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
	return os.Rename(name, p)
}
