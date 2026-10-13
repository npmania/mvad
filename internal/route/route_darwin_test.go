//go:build darwin

package route

import (
	"errors"
	"net/netip"
	"testing"
)

const (
	getDefault = `   route to: default
destination: default
       mask: default
    gateway: 192.168.8.1
  interface: en4
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>
 recvpipe  sendpipe  ssthresh  rtt,msec    rttvar  hopcount      mtu     expire
       0         0         0         0         0         0      1500         0
`
	getOnLink = `   route to: 192.168.8.1
destination: 192.168.8.1
  interface: en4
      flags: <UP,HOST,DONE,LLINFO,WASCLONED,IFSCOPE,IFREF,ROUTER>
 recvpipe  sendpipe  ssthresh  rtt,msec    rttvar  hopcount      mtu     expire
       0         0         0         0         0         0      1500      1192
`
	getScoped6 = `   route to: ::
destination: ::
       mask: default
    gateway: fe80::%utun1
  interface: utun1
      flags: <UP,GATEWAY,DONE,PRCLONING,IFSCOPE,GLOBAL>
 recvpipe  sendpipe  ssthresh  rtt,msec    rttvar  hopcount      mtu     expire
       0         0         0         0         0         0      1500         0
`
	notInTable = "route: writing to routing socket: not in table\n"
	addExists  = "route: writing to routing socket: File exists\nadd net 0.0.0.0/1: gateway utun99: File exists\n"
	addOK      = "add net 0.0.0.0/1: gateway utun99\n"
	addHostOK  = "add host 185.65.135.1: gateway 192.168.8.1\n"
)

func TestParseGet(t *testing.T) {
	tests := []struct {
		name, out, gw, dev string
	}{
		{"default", getDefault, "192.168.8.1", "en4"},
		{"on-link", getOnLink, "", "en4"},
		{"scoped v6", getScoped6, "fe80::%utun1", "utun1"},
		{"not in table", notInTable, "", ""},
	}
	for _, tt := range tests {
		gw, dev := parseGet(tt.out)
		if gw != tt.gw || dev != tt.dev {
			t.Errorf("%s: parseGet = %q, %q, want %q, %q", tt.name, gw, dev, tt.gw, tt.dev)
		}
		if gw != "" {
			if _, err := netip.ParseAddr(gw); err != nil {
				t.Errorf("%s: gateway %q: %v", tt.name, gw, err)
			}
		}
	}
}

func TestFailure(t *testing.T) {
	tests := []struct {
		out, want string
	}{
		{getDefault, ""},
		{getOnLink, ""},
		{addOK, ""},
		{addHostOK, ""},
		{notInTable, "writing to routing socket: not in table"},
		{addExists, "writing to routing socket: File exists"},
		{"   route to: 10.0.0.1\nroute: message indicates error 3: No such process\n", "message indicates error 3: No such process"},
	}
	for _, tt := range tests {
		if got := failure(tt.out); got != tt.want {
			t.Errorf("failure(%q) = %q, want %q", tt.out, got, tt.want)
		}
	}
}

func TestMissing(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("route delete -inet 0.0.0.0/1: writing to routing socket: not in table"), true},
		{errors.New("route add -inet 0.0.0.0/1 -interface utun99: writing to routing socket: File exists"), false},
		{errors.New("route add -inet 0.0.0.0/1 -interface utun99: exit status 77: route: must be root to alter routing table"), false},
	}
	for _, tt := range tests {
		if got := missing(tt.err); got != tt.want {
			t.Errorf("missing(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
