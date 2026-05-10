//go:build linux

package notify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/npmania/mvad/internal/config"
)

func Send(title, body string) error {
	bin, err := exec.LookPath("notify-send")
	if err != nil {
		return nil
	}

	cu, err := config.ResolveCallingUser()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if cu != nil {
		runtimeDir := fmt.Sprintf("/run/user/%d", cu.UID)
		sock := filepath.Join(runtimeDir, "bus")
		if _, err := os.Stat(sock); err != nil {
			return nil
		}
		cmd := exec.CommandContext(ctx, bin, "-a", "mvad", title, body)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{
				Uid: uint32(cu.UID),
				Gid: uint32(cu.GID),
			},
		}
		cmd.Env = []string{
			"DBUS_SESSION_BUS_ADDRESS=unix:path=" + sock,
			"XDG_RUNTIME_DIR=" + runtimeDir,
			"HOME=" + cu.Home,
		}
		return cmd.Run()
	}

	if !hasSessionBus() {
		return nil
	}
	cmd := exec.CommandContext(ctx, bin, "-a", "mvad", title, body)
	cmd.Env = os.Environ()
	return cmd.Run()
}

func hasSessionBus() bool {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "bus"))
	return err == nil
}
