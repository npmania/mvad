//go:build linux

package route

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
)

func set(iface string, endpoint netip.Addr) error {
	if endpoint.IsValid() {
		if err := pin(endpoint); err != nil {
			return err
		}
	}
	if err := ip("-4", "route", "replace", "default", "dev", iface); err != nil {
		if endpoint.IsValid() {
			_ = unpin(endpoint)
		}
		return err
	}
	if err := ip("-6", "route", "replace", "default", "dev", iface); err != nil {
		_ = ip("-4", "route", "del", "default", "dev", iface)
		if endpoint.IsValid() {
			_ = unpin(endpoint)
		}
		return err
	}
	return nil
}

func unset(iface string, endpoint netip.Addr) error {
	err4 := ipMissingOK("-4", "route", "del", "default", "dev", iface)
	err6 := ipMissingOK("-6", "route", "del", "default", "dev", iface)
	var errp error
	if endpoint.IsValid() {
		errp = unpin(endpoint)
	}
	return errors.Join(err4, err6, errp)
}

func pin(addr netip.Addr) error {
	gw, dev, err := routeFor(addr)
	if err != nil {
		return err
	}
	args := []string{"route", "add", hostPrefix(addr)}
	if gw != "" {
		args = append(args, "via", gw)
	}
	args = append(args, "dev", dev)
	return ip(args...)
}

func unpin(addr netip.Addr) error {
	return ipMissingOK("route", "del", hostPrefix(addr))
}

// ipMissingOK tolerates "already gone" so teardown stays idempotent.
func ipMissingOK(args ...string) error {
	err := ip(args...)
	if err == nil {
		return nil
	}
	s := err.Error()
	if strings.Contains(s, "Cannot find device") || strings.Contains(s, "No such process") {
		return nil
	}
	return err
}

func hostPrefix(addr netip.Addr) string {
	if addr.Is4() {
		return addr.String() + "/32"
	}
	return addr.String() + "/128"
}

func routeFor(addr netip.Addr) (gw, dev string, err error) {
	out, err := ipOutput("-j", "route", "get", addr.String())
	if err != nil {
		return "", "", err
	}
	var rs []struct {
		Gateway string `json:"gateway"`
		Dev     string `json:"dev"`
	}
	if err := json.Unmarshal(out, &rs); err != nil {
		return "", "", fmt.Errorf("ip -j route get %s: %w", addr, err)
	}
	if len(rs) == 0 || rs[0].Dev == "" {
		return "", "", fmt.Errorf("no route to %s", addr)
	}
	return rs[0].Gateway, rs[0].Dev, nil
}

func ip(args ...string) error {
	_, err := ipOutput(args...)
	return err
}

func ipOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("ip", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return out, nil
}
