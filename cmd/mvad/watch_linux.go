//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

type routeWatcher struct {
	conn   *netlink.Conn
	self   atomic.Int32
	events chan struct{}
}

func newRouteWatcher(iface string) (*routeWatcher, error) {
	conn, err := netlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		return nil, fmt.Errorf("watch netlink routes: %w", err)
	}
	for _, g := range []uint32{unix.RTNLGRP_LINK, unix.RTNLGRP_IPV4_ROUTE, unix.RTNLGRP_IPV6_ROUTE} {
		if err := conn.JoinGroup(g); err != nil {
			conn.Close()
			return nil, fmt.Errorf("watch netlink routes: %w", err)
		}
	}
	w := &routeWatcher{conn: conn, events: make(chan struct{}, 16)}
	w.refresh(iface)
	go w.run()
	return w, nil
}

func (w *routeWatcher) Close() { w.conn.Close() }

func (w *routeWatcher) run() {
	for {
		msgs, err := w.conn.Receive()
		if errors.Is(err, unix.ENOBUFS) {
			// The socket overflowed; what it dropped may have mattered.
			select {
			case w.events <- struct{}{}:
			default:
			}
			continue
		}
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
