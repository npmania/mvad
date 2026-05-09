//go:build linux

package route

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func set(iface string) error {
	if err := ip("-4", "route", "replace", "default", "dev", iface); err != nil {
		return err
	}
	return ip("-6", "route", "replace", "default", "dev", iface)
}

func unset(iface string) error {
	err4 := ip("-4", "route", "del", "default", "dev", iface)
	err6 := ip("-6", "route", "del", "default", "dev", iface)
	return errors.Join(err4, err6)
}

func ip(args ...string) error {
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}
