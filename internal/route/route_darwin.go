//go:build darwin

package route

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
)

// halves outrank the default route, which configd owns and rewrites.
var halves = []struct{ fam, dst string }{
	{"-inet", "0.0.0.0/1"},
	{"-inet", "128.0.0.0/1"},
	{"-inet6", "::/1"},
	{"-inet6", "8000::/1"},
}

func set(iface string, endpoint netip.Addr) error {
	if endpoint.IsValid() {
		if err := pin(endpoint); err != nil {
			return err
		}
	}
	for i, h := range halves {
		if _, err := route("add", h.fam, h.dst, "-interface", iface); err != nil {
			for _, a := range halves[:i] {
				_ = drop(iface, a.fam, a.dst)
			}
			if endpoint.IsValid() {
				_ = unpin(endpoint)
			}
			return err
		}
	}
	return nil
}

func unset(iface string, endpoint netip.Addr) error {
	var errs []error
	for _, h := range halves {
		errs = append(errs, drop(iface, h.fam, h.dst))
	}
	if endpoint.IsValid() {
		errs = append(errs, unpin(endpoint))
	}
	return errors.Join(errs...)
}

// drop deletes dst if it routes through iface. Delete matches on
// destination and mask alone, and get falls back to the default
// route when dst is absent.
func drop(iface, fam, dst string) error {
	out, err := route("get", fam, dst)
	if missing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, dev := parseGet(out); dev != iface {
		return nil
	}
	return routeMissingOK("delete", fam, dst)
}

// configd already routes 224.0.0.0/4 out each LAN interface, which
// outranks the halves; deleting it would remove configd's own route.
func setLAN(string) error { return nil }
func unsetLAN() error     { return nil }

// pin keeps the endpoint on its pre-tunnel path. Without -host,
// route(8) reads addresses like 193.32.249.0 as classful networks.
func pin(addr netip.Addr) error {
	fam := family(addr)
	out, err := route("get", fam, "-host", addr.String())
	if err != nil {
		return err
	}
	gw, dev := parseGet(out)
	if dev == "" {
		return fmt.Errorf("no route to %s", addr)
	}
	args := []string{"add", fam, "-host", addr.String()}
	if gw != "" {
		args = append(args, gw)
	} else {
		args = append(args, "-interface", dev)
	}
	_, err = route(args...)
	return err
}

func unpin(addr netip.Addr) error {
	return routeMissingOK("delete", family(addr), "-host", addr.String())
}

func family(addr netip.Addr) string {
	if addr.Is4() {
		return "-inet"
	}
	return "-inet6"
}

func defaultRoute() (netip.Addr, string, error)  { return defaultRouteFam("-inet") }
func defaultRoute6() (netip.Addr, string, error) { return defaultRouteFam("-inet6") }

func defaultRouteFam(fam string) (netip.Addr, string, error) {
	out, err := route("get", fam, "default")
	if err != nil && !missing(err) {
		return netip.Addr{}, "", err
	}
	gw, dev := parseGet(out)
	addr, err := netip.ParseAddr(gw)
	if err != nil || dev == "" {
		return netip.Addr{}, "", errors.New("no default route")
	}
	return addr, dev, nil
}

// parseGet returns the gateway and interface from route get output.
// The gateway line is absent for on-link and interface routes.
func parseGet(out string) (gw, dev string) {
	for line := range strings.SplitSeq(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch k {
		case "gateway":
			gw = strings.TrimSpace(v)
		case "interface":
			dev = strings.TrimSpace(v)
		}
	}
	return gw, dev
}

// routeMissingOK tolerates "already gone" so teardown stays idempotent.
func routeMissingOK(args ...string) error {
	_, err := route(args...)
	if missing(err) {
		return nil
	}
	return err
}

func missing(err error) bool {
	return err != nil && strings.HasSuffix(err.Error(), "not in table")
}

// route runs route(8), which exits 0 even when the kernel rejects
// the request and only says so in a warning.
func route(args ...string) (string, error) {
	cmd := exec.Command("/sbin/route", append([]string{"-n"}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("route %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	if msg := failure(string(out)); msg != "" {
		return "", fmt.Errorf("route %s: %s", strings.Join(args, " "), msg)
	}
	return string(out), nil
}

// failure returns the first warning in route(8) output, if any.
func failure(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if msg, ok := strings.CutPrefix(line, "route: "); ok {
			return msg
		}
	}
	return ""
}
