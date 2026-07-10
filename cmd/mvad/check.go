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
	if tunnelDead(s) {
		return &exitErr{code: 1, err: errors.New("tunnel probe failed")}
	}
	return nil
}

// tunnelDead reports whether a tunnel that is Up carries no traffic. A
// fresh WireGuard handshake means the relay answered within the last
// rekey interval, so the data plane is live even if the in-tunnel
// resolver is momentarily unreachable; only when there is no such
// corroboration does a failed probe condemn the tunnel. This keeps a
// resolver hiccup from tearing down a working session.
func tunnelDead(s status.Snapshot) bool {
	if !s.LastHandshake.IsZero() && time.Since(s.LastHandshake) < handshakeFresh {
		return false
	}
	return probeTunnel() != nil
}

// handshakeFresh bounds how recent a handshake still counts as the
// relay being alive; WireGuard rekeys about every two minutes.
const handshakeFresh = 150 * time.Second

// probeTunnel sends a DNS query to the in-tunnel resolvers, on a marked
// socket in split mode so it rides the split routing. It tries each
// resolver with a growing timeout so one dropped query or a slow first
// recursion does not read as a dead tunnel; only when every attempt
// fails does it report an error.
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
	var last error
	for _, timeout := range []time.Duration{3 * time.Second, 5 * time.Second} {
		for _, res := range splitDNS {
			r := net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					return d.DialContext(ctx, network, net.JoinHostPort(res.String(), "53"))
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			_, err := r.LookupHost(ctx, "mullvad.net")
			cancel()
			if err == nil {
				return nil
			}
			last = err
		}
	}
	return last
}
