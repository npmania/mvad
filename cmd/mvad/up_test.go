//go:build linux || darwin

package main

import (
	"net"
	"testing"
	"time"
)

func TestRefresh(t *testing.T) {
	ifs, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	var lo *net.Interface
	for i := range ifs {
		if ifs[i].Flags&net.FlagLoopback != 0 {
			lo = &ifs[i]
			break
		}
	}
	if lo == nil {
		t.Skip("no loopback interface")
	}
	w := &routeWatcher{events: make(chan struct{}, 16)}
	w.events <- struct{}{}
	w.events <- struct{}{}
	w.refresh(lo.Name)
	if got := int(w.self.Load()); got != lo.Index {
		t.Errorf("self = %d, want %d", got, lo.Index)
	}
	if len(w.events) != 0 {
		t.Errorf("%d events left queued", len(w.events))
	}
	retryDelay = time.Millisecond
	w.refresh("nonexistent0")
	if got := int(w.self.Load()); got != lo.Index {
		t.Errorf("self after missing iface = %d, want %d", got, lo.Index)
	}
	select {
	case <-w.events:
	case <-time.After(5 * time.Second):
		t.Error("no retry after a failed rebuild")
	}
}
