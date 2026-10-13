//go:build darwin

package wg

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/ipc"
	"golang.zx2c4.com/wireguard/tun"
)

const iface = "utun99"

// sockPath is where ipc puts the UAPI socket and wgctrl finds it.
func sockPath(name string) string {
	return "/var/run/wireguard/" + name + ".sock"
}

func up(cfg Config) error {
	if err := validate(cfg); err != nil {
		return err
	}
	exited, err := start(cfg.Name, cfg.MTU)
	if err != nil {
		return err
	}
	// ipc.UAPIOpen's listener stays open in this process: with the child
	// gone, wgctrl would wait on a socket nobody serves.
	errc := make(chan error, 1)
	go func() { errc <- configure(cfg) }()
	select {
	case err = <-errc:
	case err = <-exited:
		err = fmt.Errorf("wg: mvad __wg %s exited: %v", cfg.Name, err)
	case <-time.After(5 * time.Second):
		err = fmt.Errorf("wg: %s: no answer from mvad __wg", cfg.Name)
	}
	if err != nil {
		down(cfg.Name)
		return err
	}
	// A utun is point-to-point: inet needs a destination, and the local
	// address serves.
	if a := cfg.Address; a.IsValid() {
		if err := run("/sbin/ifconfig", cfg.Name, "inet", a.String(), a.Addr().String(), "alias"); err != nil {
			down(cfg.Name)
			return err
		}
	}
	if a := cfg.Address6; a.IsValid() {
		if err := run("/sbin/ifconfig", cfg.Name, "inet6", a.String(), "alias"); err != nil {
			down(cfg.Name)
			return err
		}
	}
	return nil
}

// start creates the utun and hands it, with its UAPI listener, to a
// detached mvad __wg running the userspace device. The returned
// channel yields the child's exit.
func start(name string, mtu int) (<-chan error, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("wg: %w", err)
	}
	t, err := createTUN(name, mtu)
	if err != nil {
		return nil, err
	}
	uapi, err := ipc.UAPIOpen(name)
	if err != nil {
		t.Close()
		return nil, fmt.Errorf("wg: uapi %s: %w", name, err)
	}
	// Stdio stays /dev/null: osascript's do shell script waits until
	// its output pipes close.
	cmd := exec.Command(self, "__wg", name)
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{t.File(), uapi}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	// The utun lives until its last fd closes, which must be the child's.
	uapi.Close()
	t.Close()
	if err != nil {
		os.Remove(sockPath(name))
		return nil, fmt.Errorf("wg: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return exited, nil
}

// createTUN retries a busy unit whose interface is already gone: the
// kernel frees the unit only after the old interface finishes detaching.
func createTUN(name string, mtu int) (tun.Device, error) {
	for i := 0; ; i++ {
		t, err := tun.CreateTUN(name, mtu)
		if err == nil {
			return t, nil
		}
		if errors.Is(err, syscall.EBUSY) {
			if _, err := net.InterfaceByName(name); err == nil {
				return nil, fmt.Errorf("wg: %s already exists", name)
			}
			if i < 20 {
				time.Sleep(50 * time.Millisecond)
				continue
			}
		}
		return nil, fmt.Errorf("wg: create %s: %w", name, err)
	}
}

func down(name string) error {
	if name == "" {
		return errors.New("wg: empty interface name")
	}
	// The __wg child exits when its socket goes, taking the utun along.
	if err := os.Remove(sockPath(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("wg: %w", err)
	}
	for range 60 {
		if _, err := net.InterfaceByName(name); err != nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("wg: %s still present", name)
}

func serve(name string) error {
	// os.NewFile polls only a nonblocking fd, and dev.Close must be
	// able to interrupt the tun read.
	if err := syscall.SetNonblock(3, true); err != nil {
		return err
	}
	t, err := tun.CreateTUNFromFile(os.NewFile(3, ""), 0)
	if err != nil {
		return err
	}
	dev := device.NewDevice(t, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, ""))
	defer dev.Close()
	l, err := ipc.UAPIListen(name, os.NewFile(4, ""))
	if err != nil {
		return err
	}
	defer l.Close() // unlinks the socket
	// Up by hand: the utun was up before tun's route listener started,
	// and its EventUp never comes.
	if err := dev.Up(); err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				errc <- err
				return
			}
			go dev.IpcHandle(c)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	select {
	case <-sig:
	case err := <-errc:
		// A removed socket is how down asks us to go.
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	case <-dev.Wait():
	}
	return nil
}
