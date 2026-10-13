//go:build darwin

package notify

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/npmania/mvad/internal/config"
)

func Send(title, body string) error {
	script := []string{
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, body,
	}

	cu, err := config.ResolveCallingUser()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if cu != nil {
		// asuser joins the user's GUI session but keeps root's uid;
		// sudo drops it.
		uid := strconv.Itoa(cu.UID)
		args := append([]string{"asuser", uid, "/usr/bin/sudo", "-u", "#" + uid, "/usr/bin/osascript"}, script...)
		return exec.CommandContext(ctx, "/bin/launchctl", args...).Run()
	}

	if os.Geteuid() == 0 {
		return nil
	}
	return exec.CommandContext(ctx, "/usr/bin/osascript", script...).Run()
}
