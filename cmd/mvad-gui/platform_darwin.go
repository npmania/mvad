//go:build darwin

package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"gioui.org/io/system"
)

const (
	opener   = "/usr/bin/open"
	trayName = "menu bar"
)

// A window reopened from the menu-bar item belongs to an inactive app.
const openActions = system.ActionRaise

// Per-user and short: sun_path holds at most 103 bytes.
func instanceDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return os.TempDir()
	}
	return d
}

// asRoot runs argv as one root shell command, each word through
// AppleScript's quoted form of.
const asRoot = `on run argv
	set c to ""
	repeat with a in argv
		set c to c & quoted form of a & " "
	end repeat
	do shell script c & "2>&1" with prompt "mvad wants to change network settings." with administrator privileges without altering line endings
end run`

func runPriv(ctx context.Context, args ...string) (string, error) {
	mvad, err := exec.LookPath(mvadCmd)
	if err != nil {
		return "", err
	}
	// env would take the path for an assignment.
	if strings.Contains(mvad, "=") {
		return "", fmt.Errorf("%s: '=' in path", mvad)
	}
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	// Root mvad finds the caller's config through SUDO_USER.
	argv := []string{"-e", asRoot, "--", "/usr/bin/env", "SUDO_USER=" + u.Username, mvad}
	out, err := exec.CommandContext(ctx, "/usr/bin/osascript", append(argv, args...)...).CombinedOutput()
	return osaResult(out, err)
}

// osaErr matches "N:M: execution error: <output> (<status>)".
var osaErr = regexp.MustCompile(`(?s)execution error: (.*) \((-?\d+)\)\n?$`)

func osaResult(out []byte, err error) (string, error) {
	if err == nil {
		return strings.TrimSuffix(string(out), "\n"), nil
	}
	m := osaErr.FindSubmatch(out)
	if m == nil {
		return string(out), err
	}
	// -128 is the dialog's Cancel; its message is localized.
	if string(m[2]) == "-128" {
		return "", errors.New("authentication canceled")
	}
	return string(m[1]), fmt.Errorf("exit status %s", m[2])
}

const hasLoginItem = true

func loginAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", "io.github.npmania.mvad-gui.plist"), nil
}

func loginItem() bool {
	p, err := loginAgentPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// setLoginItem installs a per-user LaunchAgent that starts the app
// in the menu bar at login.
func setLoginItem(on bool) error {
	p, err := loginAgentPath()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Gone after a reboot: a mounted dmg, or Gatekeeper's translocated copy.
	if strings.HasPrefix(exe, "/Volumes/") || strings.Contains(exe, "/AppTranslocation/") {
		return errors.New("move mvad to Applications first")
	}
	var b strings.Builder
	xml.EscapeText(&b, []byte(exe))
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(fmt.Sprintf(loginAgent, b.String())), 0644)
}

const loginAgent = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>io.github.npmania.mvad-gui</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>-hidden</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`
