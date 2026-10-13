//go:build darwin

package main

/*
void trayStart(void);
void traySet(int up, char *tip);
void trayAdd(int tag, int child, char *label);
void trayCommit(void);
int appDark(void);
*/
import "C"

import (
	"context"
	"sync"
)

// statusItem is the menu-bar item. The ObjC side keeps the AppKit
// objects and reports clicks through trayClicked.
type statusItem struct {
	cmds chan<- trayCmd

	mu   sync.Mutex
	menu []menuItem
	wake func()
}

var theStatusItem *statusItem

// The first item is always Show: with no Dock icon it is the way back
// to a buried window. windowState goes unread.
func startTray(ctx context.Context, polls <-chan pollResult, _ <-chan bool, cmds chan<- trayCmd, favorites []string) (tray, error) {
	t := &statusItem{cmds: cmds}
	theStatusItem = t
	C.trayStart()
	t.set(false)
	t.setMenu(buildTrayMenu(favorites, false))
	go t.loop(ctx, polls)
	return t, nil
}

func (t *statusItem) loop(ctx context.Context, polls <-chan pollResult) {
	var up bool
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-polls:
			if u := r.err == nil && r.snap.Up; u != up {
				up = u
				t.set(up)
			}
		}
	}
}

func (t *statusItem) set(up bool) {
	tip := "mvad: disconnected"
	if up {
		tip = "mvad: connected"
	}
	C.traySet(cint(up), C.CString(tip))
}

// setMenu sends items to AppKit one by one, under t.mu: the main queue
// builds one menu at a time.
func (t *statusItem) setMenu(items []menuItem) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.menu = items
	for _, it := range items {
		addItem(it, false)
		for _, c := range it.children {
			addItem(c, true)
		}
	}
	C.trayCommit()
}

func addItem(it menuItem, child bool) {
	var label *C.char
	if !it.sep {
		label = C.CString(it.label)
	}
	C.trayAdd(C.int(it.id), cint(child), label)
}

func (t *statusItem) setWake(fn func()) {
	t.mu.Lock()
	t.wake = fn
	t.mu.Unlock()
}

func (t *statusItem) setDark(bool) {}

func (t *statusItem) shutdown() {}

// trayClicked runs on the main thread. It reports whether the app
// should come to the front, which is whenever the window opens.
//
//export trayClicked
func trayClicked(tag C.int) C.int {
	t := theStatusItem
	t.mu.Lock()
	it, ok := findByID(t.menu, int32(tag))
	t.mu.Unlock()
	if !ok || it.sep {
		return 0
	}
	c := it.cmd
	if !t.send(c) {
		return 0
	}
	switch c.kind {
	case cmdQuit, cmdDisconnect, cmdReconnect:
		return 0
	}
	return 1
}

// trayReopen runs on the main thread when Finder or Spotlight opens
// the app while it runs; LaunchServices sends that instead of starting
// a second instance.
//
//export trayReopen
func trayReopen() {
	theStatusItem.send(trayCmd{kind: cmdShow})
}

func (t *statusItem) send(c trayCmd) bool {
	t.mu.Lock()
	wake := t.wake
	t.mu.Unlock()
	select {
	case t.cmds <- c:
		if wake != nil {
			wake()
		}
		return true
	default:
		return false
	}
}

func systemDark() bool { return C.appDark() != 0 }

func cint(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
