//go:build darwin

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync/atomic"

	"golang.org/x/sys/unix"

	"github.com/npmania/mvad/internal/firewall"
)

type routeWatcher struct {
	f          *os.File
	self       atomic.Int32
	events     chan struct{}
	killSwitch func() bool
}

func newRouteWatcher(iface string) (*routeWatcher, error) {
	fd, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("watch routing socket: %w", err)
	}
	unix.CloseOnExec(fd)
	// Nonblocking: a pollable os.File, whose Close unblocks Read.
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("watch routing socket: %w", err)
	}
	w := &routeWatcher{f: os.NewFile(uintptr(fd), "route"), events: make(chan struct{}, 16), killSwitch: firewall.Installed}
	w.refresh(iface)
	go w.run()
	return w, nil
}

func (w *routeWatcher) Close() { w.f.Close() }

func (w *routeWatcher) run() {
	b := make([]byte, 64<<10)
	for {
		n, err := w.f.Read(b)
		if err != nil {
			return
		}
		for m := b[:n]; len(m) >= 4; {
			l := int(binary.NativeEndian.Uint16(m))
			if l < 4 || l > len(m) {
				break
			}
			if w.interesting(m[:l]) {
				select {
				case w.events <- struct{}{}:
				default:
				}
			}
			m = m[l:]
		}
	}
}

// interesting reports whether routing message m calls for a reconnect:
// an unscoped default route changed on another interface, or the
// tunnel went down under its kill-switch. Other link and address
// messages are noise; awdl0 and llw0 flap constantly.
func (w *routeWatcher) interesting(m []byte) bool {
	if len(m) < 4 || m[2] != unix.RTM_VERSION {
		return false
	}
	switch m[3] {
	case unix.RTM_ADD, unix.RTM_DELETE, unix.RTM_CHANGE:
		// rt_msghdr: index u16 @4, flags i32 @8, addrs i32 @12, errno i32 @24.
		if len(m) < unix.SizeofRtMsghdr {
			return false
		}
		flags := binary.NativeEndian.Uint32(m[8:])
		if flags&(unix.RTF_IFSCOPE|unix.RTF_WASCLONED|unix.RTF_LLINFO) != 0 || binary.NativeEndian.Uint32(m[24:]) != 0 {
			return false
		}
		dst, mask, link := sockaddrs(m[unix.SizeofRtMsghdr:], binary.NativeEndian.Uint32(m[12:]))
		// 0.0.0.0/1 has a zero destination too; only the mask tells.
		if len(dst) < 2 || dst[1] != unix.AF_INET && dst[1] != unix.AF_INET6 || !zero(dst) || !zero(mask) {
			return false
		}
		// route(8) leaves rtm_index 0 unless scoped and names an
		// -interface route's interface in an AF_LINK gateway.
		idx := int(binary.NativeEndian.Uint16(m[4:]))
		if idx == 0 {
			idx = link
		}
		return idx == 0 || idx != int(w.self.Load())
	case unix.RTM_IFINFO:
		// if_msghdr: flags i32 @8, index u16 @12.
		if len(m) < 14 {
			return false
		}
		down := binary.NativeEndian.Uint32(m[8:])&unix.IFF_UP == 0
		idx := int32(binary.NativeEndian.Uint16(m[12:]))
		// A disconnect drops the kill-switch first; one still up means
		// mvad __wg died.
		return down && idx == w.self.Load() && w.killSwitch()
	}
	return false
}

// sockaddrs returns the destination and netmask among the sockaddrs in
// b, present per the RTA_* bits in addrs, and the index of the first
// AF_LINK one. Each takes a multiple of 4 bytes, at least 4; masks are
// trimmed, down to zero length for /0.
func sockaddrs(b []byte, addrs uint32) (dst, mask []byte, link int) {
	for i := 0; i < unix.RTAX_MAX && len(b) > 0; i++ {
		if addrs&(1<<i) == 0 {
			continue
		}
		n := int(b[0])
		sa := b[:min(n, len(b))]
		switch i {
		case unix.RTAX_DST:
			dst = sa
		case unix.RTAX_NETMASK:
			mask = sa
		}
		if link == 0 && len(sa) >= 4 && sa[1] == unix.AF_LINK {
			link = int(binary.NativeEndian.Uint16(sa[2:]))
		}
		b = b[min(max(4, (n+3)&^3), len(b)):]
	}
	return dst, mask, link
}

// zero reports whether sockaddr sa holds an all-zero address.
// An absent or empty sockaddr counts.
func zero(sa []byte) bool {
	for _, c := range sa[min(2, len(sa)):] {
		if c != 0 {
			return false
		}
	}
	return true
}
