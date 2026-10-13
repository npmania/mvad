//go:build darwin

package main

import (
	"encoding/binary"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// rtmsg builds a route message carrying the sockaddrs in sas, keyed by
// RTAX index; a nil entry is an empty sockaddr.
func rtmsg(typ uint8, index uint16, flags uint32, errno int32, sas map[int][]byte) []byte {
	m := make([]byte, unix.SizeofRtMsghdr)
	m[2] = unix.RTM_VERSION
	m[3] = typ
	binary.NativeEndian.PutUint16(m[4:], index)
	binary.NativeEndian.PutUint32(m[8:], flags)
	binary.NativeEndian.PutUint32(m[24:], uint32(errno))
	var addrs uint32
	for i := range unix.RTAX_MAX {
		sa, ok := sas[i]
		if !ok {
			continue
		}
		addrs |= 1 << i
		p := make([]byte, max(4, (len(sa)+3)&^3))
		copy(p, sa)
		m = append(m, p...)
	}
	binary.NativeEndian.PutUint32(m[12:], addrs)
	binary.NativeEndian.PutUint16(m, uint16(len(m)))
	return m
}

func ifmsg(typ uint8, index uint16, flags uint32) []byte {
	m := make([]byte, unix.SizeofIfMsghdr)
	binary.NativeEndian.PutUint16(m, uint16(len(m)))
	m[2] = unix.RTM_VERSION
	m[3] = typ
	binary.NativeEndian.PutUint32(m[8:], flags)
	binary.NativeEndian.PutUint16(m[12:], index)
	return m
}

func inet4(a ...byte) []byte {
	sa := make([]byte, 16)
	sa[0], sa[1] = 16, unix.AF_INET
	copy(sa[4:], a)
	return sa
}

func inet6(a ...byte) []byte {
	sa := make([]byte, 28)
	sa[0], sa[1] = 28, unix.AF_INET6
	copy(sa[8:], a)
	return sa
}

// mask4 and mask6 build trimmed masks: the kernel drops trailing zero bytes.
func mask4(a ...byte) []byte { return append([]byte{byte(4 + len(a)), unix.AF_INET, 0, 0}, a...) }
func mask6(a ...byte) []byte {
	return append([]byte{byte(8 + len(a)), unix.AF_INET6, 0, 0, 0, 0, 0, 0}, a...)
}

func link(index uint16) []byte {
	sa := make([]byte, 20)
	sa[0], sa[1] = 20, unix.AF_LINK
	binary.NativeEndian.PutUint16(sa[2:], index)
	return sa
}

const (
	tunIndex = 30
	en0Index = 4
)

func TestInteresting(t *testing.T) {
	const gw = unix.RTF_UP | unix.RTF_GATEWAY | unix.RTF_STATIC
	tests := []struct {
		name       string
		self       int32
		killSwitch bool
		m          []byte
		want       bool
	}{
		{"default added", tunIndex, false, rtmsg(unix.RTM_ADD, 0, gw, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: inet4(192, 168, 1, 1), unix.RTAX_NETMASK: nil,
		}), true},
		{"default deleted", tunIndex, false, rtmsg(unix.RTM_DELETE, en0Index, gw, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: inet4(192, 168, 1, 1), unix.RTAX_NETMASK: mask4(),
		}), true},
		{"v6 default changed", tunIndex, false, rtmsg(unix.RTM_CHANGE, en0Index, gw, 0, map[int][]byte{
			unix.RTAX_DST: inet6(), unix.RTAX_GATEWAY: inet6(0xfe, 0x80), unix.RTAX_NETMASK: mask6(),
		}), true},
		{"default with full zero mask", tunIndex, false, rtmsg(unix.RTM_ADD, en0Index, gw, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: inet4(10, 0, 0, 1), unix.RTAX_NETMASK: inet4(),
		}), true},
		{"default without mask", tunIndex, false, rtmsg(unix.RTM_ADD, 0, gw, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: inet4(10, 0, 0, 1),
		}), true},
		{"default on interface link", tunIndex, false, rtmsg(unix.RTM_ADD, 0, unix.RTF_UP|unix.RTF_STATIC, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: link(en0Index), unix.RTAX_NETMASK: nil,
		}), true},
		{"default while tunnel unknown", 0, false, rtmsg(unix.RTM_ADD, en0Index, gw, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: inet4(10, 0, 0, 1), unix.RTAX_NETMASK: nil,
		}), true},
		{"default on tunnel link", tunIndex, false, rtmsg(unix.RTM_ADD, 0, unix.RTF_UP|unix.RTF_STATIC, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: link(tunIndex), unix.RTAX_NETMASK: nil,
		}), false},
		{"default on tunnel index", tunIndex, false, rtmsg(unix.RTM_DELETE, tunIndex, unix.RTF_UP|unix.RTF_STATIC, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: link(tunIndex), unix.RTAX_NETMASK: nil,
		}), false},
		{"scoped default", tunIndex, false, rtmsg(unix.RTM_ADD, en0Index, gw|unix.RTF_IFSCOPE, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: inet4(192, 168, 1, 1), unix.RTAX_NETMASK: nil,
		}), false},
		{"failed request", tunIndex, false, rtmsg(unix.RTM_ADD, 0, gw, int32(unix.EEXIST), map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: inet4(192, 168, 1, 1), unix.RTAX_NETMASK: nil,
		}), false},
		{"cloned", tunIndex, false, rtmsg(unix.RTM_DELETE, en0Index, gw|unix.RTF_WASCLONED, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: inet4(192, 168, 1, 1), unix.RTAX_NETMASK: nil,
		}), false},
		{"arp entry", tunIndex, false, rtmsg(unix.RTM_ADD, en0Index, unix.RTF_UP|unix.RTF_LLINFO, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: link(en0Index),
		}), false},
		{"lower half on tunnel", tunIndex, false, rtmsg(unix.RTM_ADD, 0, unix.RTF_UP|unix.RTF_STATIC, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: link(tunIndex), unix.RTAX_NETMASK: mask4(0x80),
		}), false},
		{"lower half elsewhere", tunIndex, false, rtmsg(unix.RTM_ADD, en0Index, gw, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: inet4(10, 0, 0, 1), unix.RTAX_NETMASK: mask4(0x80),
		}), false},
		{"upper half", tunIndex, false, rtmsg(unix.RTM_DELETE, en0Index, unix.RTF_UP|unix.RTF_STATIC, 0, map[int][]byte{
			unix.RTAX_DST: inet4(0x80), unix.RTAX_GATEWAY: link(en0Index), unix.RTAX_NETMASK: mask4(0x80),
		}), false},
		{"v6 lower half", tunIndex, false, rtmsg(unix.RTM_ADD, en0Index, unix.RTF_UP|unix.RTF_STATIC, 0, map[int][]byte{
			unix.RTAX_DST: inet6(), unix.RTAX_GATEWAY: link(en0Index), unix.RTAX_NETMASK: mask6(0x80),
		}), false},
		{"endpoint pin", tunIndex, false, rtmsg(unix.RTM_ADD, 0, gw|unix.RTF_HOST, 0, map[int][]byte{
			unix.RTAX_DST: inet4(185, 213, 154, 68), unix.RTAX_GATEWAY: inet4(192, 168, 1, 1),
		}), false},
		{"link destination", tunIndex, false, rtmsg(unix.RTM_ADD, en0Index, unix.RTF_UP, 0, map[int][]byte{
			unix.RTAX_DST: link(0), unix.RTAX_GATEWAY: link(en0Index),
		}), false},
		{"lookup", tunIndex, false, rtmsg(unix.RTM_GET, 0, 0, 0, map[int][]byte{
			unix.RTAX_DST: inet4(), unix.RTAX_NETMASK: nil,
		}), false},
		{"miss", tunIndex, false, rtmsg(unix.RTM_MISS, 0, 0, 0, map[int][]byte{
			unix.RTAX_DST: inet4(),
		}), false},
		{"truncated", tunIndex, false, rtmsg(unix.RTM_ADD, 0, gw, 0, nil)[:40], false},
		{"old version", tunIndex, false, func() []byte {
			m := rtmsg(unix.RTM_ADD, 0, gw, 0, map[int][]byte{unix.RTAX_DST: inet4(), unix.RTAX_NETMASK: nil})
			m[2] = unix.RTM_VERSION - 1
			return m
		}(), false},
		{"tunnel down under kill-switch", tunIndex, true, ifmsg(unix.RTM_IFINFO, tunIndex, unix.IFF_POINTOPOINT|unix.IFF_MULTICAST), true},
		{"tunnel down by disconnect", tunIndex, false, ifmsg(unix.RTM_IFINFO, tunIndex, unix.IFF_POINTOPOINT|unix.IFF_MULTICAST), false},
		{"old tunnel down", tunIndex + 1, true, ifmsg(unix.RTM_IFINFO, tunIndex, unix.IFF_POINTOPOINT), false},
		{"tunnel up", tunIndex, false, ifmsg(unix.RTM_IFINFO, tunIndex, unix.IFF_UP|unix.IFF_RUNNING), false},
		{"other link down", tunIndex, false, ifmsg(unix.RTM_IFINFO, en0Index, unix.IFF_BROADCAST), false},
		{"other link up", tunIndex, false, ifmsg(unix.RTM_IFINFO, en0Index, unix.IFF_UP), false},
		{"tunnel address gone", tunIndex, false, ifmsg(unix.RTM_DELADDR, tunIndex, 0)[:unix.SizeofIfaMsghdr], false},
		{"link info truncated", tunIndex, false, ifmsg(unix.RTM_IFINFO, tunIndex, 0)[:12], false},
	}
	for _, tt := range tests {
		w := routeWatcher{killSwitch: func() bool { return tt.killSwitch }}
		w.self.Store(tt.self)
		if got := w.interesting(tt.m); got != tt.want {
			t.Errorf("%s: interesting = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestRun(t *testing.T) {
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer wr.Close()
	w := &routeWatcher{f: r, events: make(chan struct{}, 16)}
	w.self.Store(tunIndex)
	done := make(chan struct{})
	go func() {
		w.run()
		close(done)
	}()

	// One read holding a boring message, then the interesting one.
	var b []byte
	b = append(b, ifmsg(unix.RTM_IFINFO, en0Index, 0)...)
	b = append(b, rtmsg(unix.RTM_DELETE, en0Index, unix.RTF_UP|unix.RTF_GATEWAY, 0, map[int][]byte{
		unix.RTAX_DST: inet4(), unix.RTAX_GATEWAY: inet4(192, 168, 1, 1), unix.RTAX_NETMASK: nil,
	})...)
	if _, err := wr.Write(b); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.events:
	case <-time.After(5 * time.Second):
		t.Fatal("no event for a deleted default route")
	}

	w.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop run")
	}
}

func TestNewRouteWatcher(t *testing.T) {
	w, err := newRouteWatcher("lo0")
	if err != nil {
		t.Fatal(err)
	}
	if w.self.Load() == 0 {
		t.Error("self not set")
	}
	w.Close()
}
