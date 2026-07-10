//go:build linux

package wg

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func up(cfg Config) error {
	if cfg.Name == "" {
		return errors.New("wg: empty interface name")
	}
	if !cfg.Address.IsValid() && !cfg.Address6.IsValid() {
		return errors.New("wg: no address")
	}
	if !cfg.Endpoint.IsValid() {
		return errors.New("wg: no endpoint")
	}
	if err := run("ip", "link", "add", cfg.Name, "type", "wireguard"); err != nil {
		return err
	}
	if cfg.MTU > 0 {
		if err := run("ip", "link", "set", cfg.Name, "mtu", strconv.Itoa(cfg.MTU)); err != nil {
			run("ip", "link", "del", cfg.Name)
			return err
		}
	}
	if cfg.Address.IsValid() {
		if err := run("ip", "address", "add", cfg.Address.String(), "dev", cfg.Name); err != nil {
			run("ip", "link", "del", cfg.Name)
			return err
		}
	}
	if cfg.Address6.IsValid() {
		if err := run("ip", "address", "add", cfg.Address6.String(), "dev", cfg.Name); err != nil {
			run("ip", "link", "del", cfg.Name)
			return err
		}
	}
	c, err := wgctrl.New()
	if err != nil {
		run("ip", "link", "del", cfg.Name)
		return err
	}
	defer c.Close()
	allowed := make([]net.IPNet, len(cfg.AllowedIPs))
	for i, p := range cfg.AllowedIPs {
		allowed[i] = prefixToIPNet(p)
	}
	priv := cfg.PrivateKey
	// No FirewallMark: the device stays unmarked, so the encrypted
	// outer transport carries mark 0 and rides the main table to the
	// relay. The split fwmark and its rp_filter/guard machinery govern
	// only inner tagged traffic and its replies, never the outer UDP.
	wcfg := wgtypes.Config{
		PrivateKey:   &priv,
		ReplacePeers: true,
		Peers: []wgtypes.PeerConfig{{
			PublicKey:         cfg.PeerKey,
			Endpoint:          net.UDPAddrFromAddrPort(cfg.Endpoint),
			ReplaceAllowedIPs: true,
			AllowedIPs:        allowed,
		}},
	}
	if err := c.ConfigureDevice(cfg.Name, wcfg); err != nil {
		run("ip", "link", "del", cfg.Name)
		return err
	}
	if err := run("ip", "link", "set", cfg.Name, "up"); err != nil {
		run("ip", "link", "del", cfg.Name)
		return err
	}
	return nil
}

func down(name string) error {
	if name == "" {
		return errors.New("wg: empty interface name")
	}
	if err := run("ip", "link", "del", name); err != nil {
		if strings.Contains(err.Error(), "Cannot find device") {
			return nil
		}
		return err
	}
	return nil
}

func read(name string) (State, error) {
	c, err := wgctrl.New()
	if err != nil {
		return State{}, err
	}
	defer c.Close()
	d, err := c.Device(name)
	if err != nil {
		return State{}, err
	}
	s := State{Name: d.Name}
	if len(d.Peers) > 0 {
		p := d.Peers[0]
		s.PeerKey = p.PublicKey
		s.RxBytes = p.ReceiveBytes
		s.TxBytes = p.TransmitBytes
		s.LastHandshake = p.LastHandshakeTime
		if p.Endpoint != nil {
			s.PeerEndpoint = p.Endpoint.AddrPort()
		}
	}
	return s, nil
}

func prefixToIPNet(p netip.Prefix) net.IPNet {
	return net.IPNet{
		IP:   p.Addr().AsSlice(),
		Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
	}
}

// run formats argv into the returned error verbatim; never pass secrets.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %v: %s", name, args, err, out)
	}
	return nil
}
