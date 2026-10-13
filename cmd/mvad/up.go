//go:build linux || darwin

package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/npmania/mvad/internal/config"
	"github.com/npmania/mvad/internal/lock"
	"github.com/npmania/mvad/internal/notify"
	"github.com/npmania/mvad/internal/split"
)

func up(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageUp)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	// The watch loop holds only its own singleton lock; the transition
	// lock is taken per reconnect so split management (add-ip, refresh)
	// can run in between.
	unlockUp, err := acquireUpLock()
	if err != nil {
		return err
	}
	defer unlockUp()
	opts, err := parseConnectOpts(args, usageUp)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(usageUp)
			return nil
		}
		return err
	}
	if opts.split && !split.Supported {
		return split.ErrUnsupported
	}
	// Split mode survives route changes on its own, and docker link
	// churn would trip the watcher into reconnect loops.
	if opts.split {
		return usagef("mvad up does not support --split; use mvad connect --split")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := checkLoggedIn(cfg); err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if err := locked(func() error { return doConnect(opts) }); err != nil {
		return err
	}

	w, err := newRouteWatcher(ifname)
	if err != nil {
		locked(doDisconnect)
		notify.Send("mvad", "tunnel down")
		return err
	}
	defer w.Close()

	for {
		select {
		case <-sigCh:
			locked(doDisconnect)
			notify.Send("mvad", "tunnel down")
			return nil
		case <-w.events:
		}
		// Suspend/resume fires events in clumps; collapse them.
		if !drainQuiet(w.events, sigCh, 2*time.Second) {
			locked(doDisconnect)
			notify.Send("mvad", "tunnel down")
			return nil
		}
		// A changed ifindex means another invocation (a failover
		// timer's reconnect) rebuilt the tunnel; the route churn was
		// its own doing.
		if iff, err := net.InterfaceByName(ifname); err == nil && iff.Index != int(w.self.Load()) {
			w.refresh(ifname)
			continue
		}
		notify.Send("mvad", "reconnecting to "+opts.relay)
		locked(func() error {
			doDisconnect()
			doConnect(opts)
			return nil
		})
		w.refresh(ifname)
	}
}

// retryDelay paces redials after a failed one.
var retryDelay = 30 * time.Second

// refresh adopts iface's index after a rebuild and drops the events
// the rebuild queued. With no iface the rebuild failed; up tries again
// after retryDelay, or at the next route change.
func (w *routeWatcher) refresh(iface string) {
	iff, err := net.InterfaceByName(iface)
	if err != nil {
		time.AfterFunc(retryDelay, func() {
			select {
			case w.events <- struct{}{}:
			default:
			}
		})
		return
	}
	w.self.Store(int32(iff.Index))
	for {
		select {
		case <-w.events:
		default:
			return
		}
	}
}

func locked(f func() error) error {
	release, err := lock.AcquireRootBlock()
	if err != nil {
		return err
	}
	defer release()
	return f()
}

func acquireUpLock() (func(), error) {
	if err := os.MkdirAll(runDir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(runDir+"/up.lock", os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("mvad up is already running; aborting")
	}
	return func() { f.Close() }, nil
}

func drainQuiet(events <-chan struct{}, sig <-chan os.Signal, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-sig:
			return false
		case <-events:
			if !t.Stop() {
				<-t.C
			}
			t.Reset(d)
		case <-t.C:
			return true
		}
	}
}
