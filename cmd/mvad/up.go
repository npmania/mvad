//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"

	"github.com/npmania/mvad/internal/config"
	"github.com/npmania/mvad/internal/lock"
	"github.com/npmania/mvad/internal/notify"
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
		return fmt.Errorf("watch netlink routes: %w", err)
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
		notify.Send("mvad", "reconnecting to "+opts.relay)
		locked(func() error {
			doDisconnect()
			doConnect(opts)
			return nil
		})
		w.refresh(ifname)
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
	if err := os.MkdirAll("/run/mvad", 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile("/run/mvad/up.lock", os.O_RDWR|os.O_CREATE, 0600)
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

type routeWatcher struct {
	conn   *netlink.Conn
	self   atomic.Int32
	events chan struct{}
}

func newRouteWatcher(iface string) (*routeWatcher, error) {
	conn, err := netlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		return nil, err
	}
	for _, g := range []uint32{unix.RTNLGRP_LINK, unix.RTNLGRP_IPV4_ROUTE, unix.RTNLGRP_IPV6_ROUTE} {
		if err := conn.JoinGroup(g); err != nil {
			conn.Close()
			return nil, err
		}
	}
	w := &routeWatcher{conn: conn, events: make(chan struct{}, 16)}
	w.refresh(iface)
	go w.run()
	return w, nil
}

func (w *routeWatcher) refresh(iface string) {
	if iff, err := net.InterfaceByName(iface); err == nil {
		w.self.Store(int32(iff.Index))
	}
}

func (w *routeWatcher) Close() { w.conn.Close() }

func (w *routeWatcher) run() {
	for {
		msgs, err := w.conn.Receive()
		if err != nil {
			return
		}
		for _, m := range msgs {
			if !w.interesting(m) {
				continue
			}
			select {
			case w.events <- struct{}{}:
			default:
			}
		}
	}
}

func (w *routeWatcher) interesting(m netlink.Message) bool {
	self := int(w.self.Load())
	switch m.Header.Type {
	case unix.RTM_NEWLINK, unix.RTM_DELLINK:
		if len(m.Data) < 8 {
			return false
		}
		idx := int(int32(binary.LittleEndian.Uint32(m.Data[4:8])))
		return idx != self
	case unix.RTM_NEWROUTE, unix.RTM_DELROUTE:
		if len(m.Data) < 12 {
			return false
		}
		if m.Data[1] != 0 {
			return false
		}
		attrs, err := netlink.UnmarshalAttributes(m.Data[12:])
		if err != nil {
			return true
		}
		for _, a := range attrs {
			if a.Type == unix.RTA_OIF && len(a.Data) >= 4 {
				idx := int(int32(binary.LittleEndian.Uint32(a.Data[:4])))
				if idx == self {
					return false
				}
			}
		}
		return true
	}
	return false
}
