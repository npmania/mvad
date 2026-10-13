//go:build !darwin

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"gioui.org/io/system"

	"github.com/godbus/dbus/v5"
)

const (
	opener   = "xdg-open"
	trayName = "tray"
)

const hasLoginItem = false

func loginItem() bool         { return false }
func setLoginItem(bool) error { return errors.ErrUnsupported }

const openActions system.Action = 0

func instanceDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return "/run/user/" + strconv.Itoa(os.Getuid())
}

func runPriv(ctx context.Context, args ...string) (string, error) {
	// pkexec must invoke the path the polkit policy is keyed on.
	full := append([]string{"mvad"}, args...)
	out, err := exec.CommandContext(ctx, "pkexec", full...).CombinedOutput()
	if err != nil {
		lo := strings.ToLower(string(out))
		if strings.Contains(lo, "polkit-agent-helper") || strings.Contains(lo, "not authorized") {
			err = errors.New("no polkit authentication agent running; install polkit-gnome (or polkit-kde-authentication-agent-1 / lxqt-policykit-agent / mate-polkit) and re-launch")
		}
	}
	return string(out), err
}

func systemDark() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}
	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	var v dbus.Variant
	if err := obj.Call("org.freedesktop.portal.Settings.Read", 0, "org.freedesktop.appearance", "color-scheme").Store(&v); err != nil {
		return false
	}
	inner, ok := v.Value().(dbus.Variant)
	if !ok {
		return false
	}
	u, ok := inner.Value().(uint32)
	return ok && u == 1
}
