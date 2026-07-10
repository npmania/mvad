//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/npmania/mvad/internal/split"
	"github.com/npmania/mvad/internal/status"
)

// checkCmd probes the tunnel end to end. Exit 1 means the tunnel
// carries no traffic; exit 3 means there is no tunnel — deliberate
// disconnects must not read as dead relays, or a failover timer would
// redial them.
func checkCmd(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageCheck)
		return nil
	}
	if len(args) != 0 {
		return usagef(usageCheck)
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	escapeSplitCgroup()
	s, err := status.Read(ifname)
	if err != nil && !errors.Is(err, status.ErrNotConnected) {
		return &exitErr{code: 2, err: err}
	}
	if !s.Up {
		return &exitErr{code: 3, err: errors.New("not connected")}
	}
	if err := probeTunnel(); err != nil {
		return &exitErr{code: 1, err: fmt.Errorf("tunnel probe failed: %v", err)}
	}
	return nil
}

// probeTunnel sends a DNS query to the in-tunnel resolver, on a
// marked socket in split mode so it rides the split routing.
func probeTunnel() error {
	mark := split.SplitMode()
	d := net.Dialer{
		Control: func(network, address string, c syscall.RawConn) error {
			if !mark {
				return nil
			}
			var serr error
			err := c.Control(func(fd uintptr) {
				serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, split.FWMark)
			})
			return errors.Join(err, serr)
		},
	}
	r := net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return d.DialContext(ctx, network, net.JoinHostPort(mullvadDNS[0].String(), "53"))
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.LookupHost(ctx, "mullvad.net")
	return err
}
