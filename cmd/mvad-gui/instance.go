package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func instancePath() string {
	d := os.Getenv("XDG_RUNTIME_DIR")
	if d == "" {
		d = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	return filepath.Join(d, "mvad-gui.sock")
}

// pingInstance returns true when another mvad-gui is already running.
// When show is true it also asks that instance to raise its window.
func pingInstance(show bool) bool {
	c, err := net.DialTimeout("unix", instancePath(), 200*time.Millisecond)
	if err != nil {
		return false
	}
	defer c.Close()
	if show {
		fmt.Fprintln(c, "show")
	}
	return true
}

func serveInstance(ctx context.Context, cmds chan<- trayCmd) error {
	p := instancePath()
	l, err := listenInstance(p)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		l.Close()
		os.Remove(p)
	}()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go handleInstance(c, cmds)
		}
	}()
	return nil
}

// listenInstance binds the instance socket. If the path is already in use,
// it pings first so a live peer is never clobbered; only a dead socket is
// removed and rebound.
func listenInstance(p string) (net.Listener, error) {
	l, err := net.Listen("unix", p)
	if err == nil {
		return l, nil
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		return nil, err
	}
	if pingInstance(false) {
		return nil, errors.New("another instance is listening")
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return net.Listen("unix", p)
}

func handleInstance(c net.Conn, cmds chan<- trayCmd) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	var buf [16]byte
	n, _ := c.Read(buf[:])
	if strings.TrimSpace(string(buf[:n])) == "show" {
		select {
		case cmds <- trayCmd{kind: cmdShow}:
		default:
		}
	}
}
