//go:build linux

package wg

import (
	"errors"
	"strconv"
	"strings"
)

const iface = "mvad-wg0"

func serve(string) error { return ErrUnsupported }

func up(cfg Config) error {
	if err := validate(cfg); err != nil {
		return err
	}
	if err := run("ip", "link", "add", cfg.Name, "type", "wireguard"); err != nil {
		return err
	}
	if cfg.MTU > 0 {
		if err := run("ip", "link", "set", cfg.Name, "mtu", strconv.Itoa(cfg.MTU)); err != nil {
			run("ip", "link", "del", cfg.Name)
			return err
		}
	}
	if cfg.Address.IsValid() {
		if err := run("ip", "address", "add", cfg.Address.String(), "dev", cfg.Name); err != nil {
			run("ip", "link", "del", cfg.Name)
			return err
		}
	}
	if cfg.Address6.IsValid() {
		if err := run("ip", "address", "add", cfg.Address6.String(), "dev", cfg.Name); err != nil {
			run("ip", "link", "del", cfg.Name)
			return err
		}
	}
	if err := configure(cfg); err != nil {
		run("ip", "link", "del", cfg.Name)
		return err
	}
	if err := run("ip", "link", "set", cfg.Name, "up"); err != nil {
		run("ip", "link", "del", cfg.Name)
		return err
	}
	return nil
}

func down(name string) error {
	if name == "" {
		return errors.New("wg: empty interface name")
	}
	if err := run("ip", "link", "del", name); err != nil {
		if strings.Contains(err.Error(), "Cannot find device") {
			return nil
		}
		return err
	}
	return nil
}
