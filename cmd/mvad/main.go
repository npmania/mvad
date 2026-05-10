package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/npmania/mvad/internal/config"
	"github.com/npmania/mvad/internal/dns"
	"github.com/npmania/mvad/internal/firewall"
	"github.com/npmania/mvad/internal/lockdown"
	"github.com/npmania/mvad/internal/mullvad"
	"github.com/npmania/mvad/internal/notify"
	"github.com/npmania/mvad/internal/route"
	"github.com/npmania/mvad/internal/status"
	"github.com/npmania/mvad/internal/udp2tcp"
	"github.com/npmania/mvad/internal/wg"
)

const (
	ifname               = "mvad-wg0"
	wireguardPort        = 51820
	udp2tcpLocalPort     = 21820
	udp2tcpPidFile       = "/run/mvad/udp2tcp.pid"
	shadowsocksLocalPort = 21822
	shadowsocksPidFile   = "/run/mvad/shadowsocks.pid"
)

var udp2tcpPorts = map[uint16]bool{80: true, 443: true, 5001: true}

var mullvadDNS = []netip.Addr{netip.MustParseAddr("10.64.0.1")}

const usageText = `usage: mvad <command> [arguments]

The commands are:

	login       log in to a Mullvad account
	logout      revoke this device and clear stored credentials
	devices     list or remove devices on this account
	rotate-key  generate a new WireGuard key for this device
	relays      list relays
	connect     connect to a relay
	disconnect  disconnect
	reconnect   reconnect to the last relay
	lockdown    install or remove the persistent kill-switch
	status      print connection status
	version     print version
`

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

type exitErr struct {
	code int
	err  error
}

func (e *exitErr) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func (e *exitErr) Unwrap() error { return e.err }

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usageText) }
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	args := flag.Args()[1:]
	cmd := flag.Arg(0)
	switch cmd {
	case "login", "logout", "relays", "rotate-key":
		if os.Geteuid() == 0 {
			fmt.Fprintln(os.Stderr, "mvad: warning: running as root; this writes config to root's home, breaking subsequent unprivileged calls")
		}
	}
	var err error
	switch cmd {
	case "login":
		err = login(args)
	case "logout":
		err = logout(args)
	case "relays":
		err = listRelays(args)
	case "devices":
		err = devices(args)
	case "rotate-key":
		err = rotateKey(args)
	case "connect":
		err = connect(args)
	case "disconnect":
		err = disconnect(args)
	case "reconnect":
		err = reconnect(args)
	case "lockdown":
		err = lockdownCmd(args)
	case "status":
		err = showStatus(args)
	case "version":
		fmt.Println("mvad", versionString())
		return
	case "__udp2tcp":
		err = udp2tcpShim(args)
	default:
		err = usagef("unknown command %q", cmd)
	}
	if err == nil {
		return
	}
	var uerr *usageError
	if errors.As(err, &uerr) {
		fmt.Fprintln(os.Stderr, "mvad:", uerr.msg)
		os.Exit(2)
	}
	var xerr *exitErr
	if errors.As(err, &xerr) {
		if xerr.err != nil {
			fmt.Fprintln(os.Stderr, "mvad:", xerr.err)
		}
		os.Exit(xerr.code)
	}
	fmt.Fprintln(os.Stderr, "mvad:", err)
	os.Exit(1)
}

func versionString() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "(devel)"
}

func login(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	keyFlag := fs.String("key", "", "base64 wireguard private key of an existing device to import")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return usagef("usage: mvad login [--key <base64-privkey>] <token>")
	}
	token := fs.Arg(0)
	if len(token) != 16 {
		return fmt.Errorf("invalid account token: must be 16 digits")
	}
	for _, c := range token {
		if c < '0' || c > '9' {
			return fmt.Errorf("invalid account token: must be 16 digits")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := mullvad.New()
	expiry, err := c.AccountExpiry(ctx, token)
	if err != nil {
		return err
	}
	if *keyFlag != "" {
		return loginImport(ctx, c, token, *keyFlag, expiry)
	}
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return err
	}
	dev, err := c.RegisterDevice(ctx, token, priv.PublicKey())
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.AccountToken = token
	cfg.DeviceID = dev.ID
	cfg.PrivateKey = priv.String()
	cfg.DeviceIPv4 = dev.IPv4
	cfg.DeviceIPv6 = dev.IPv6
	cfg.DeviceName = dev.Name
	cfg.AccountExpiry = expiry
	if err := cfg.Save(); err != nil {
		revokeErr := c.RevokeDevice(ctx, token, dev.ID)
		return errors.Join(err, revokeErr)
	}
	return nil
}

func loginImport(ctx context.Context, c *mullvad.Client, token, keyStr string, expiry time.Time) error {
	priv, err := wgtypes.ParseKey(keyStr)
	if err != nil {
		return err
	}
	devs, err := c.ListDevices(ctx, token)
	if err != nil {
		return err
	}
	pub := priv.PublicKey()
	for _, d := range devs {
		if d.PublicKey != pub {
			continue
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg.AccountToken = token
		cfg.DeviceID = d.ID
		cfg.PrivateKey = priv.String()
		cfg.DeviceIPv4 = d.IPv4
		cfg.DeviceIPv6 = d.IPv6
		cfg.DeviceName = d.Name
		cfg.AccountExpiry = expiry
		return cfg.Save()
	}
	return errors.New("no device matching that key on this account")
}

func logout(args []string) error {
	if len(args) != 0 {
		return usagef("usage: mvad logout")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AccountToken == "" || cfg.DeviceID == "" {
		return errors.New("not logged in")
	}
	s, err := status.Read(ifname)
	if err != nil && !errors.Is(err, status.ErrNotConnected) {
		return err
	}
	if s.Up {
		return errors.New("currently connected; run mvad disconnect first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := mullvad.New().RevokeDevice(ctx, cfg.AccountToken, cfg.DeviceID); err != nil {
		var apiErr *mullvad.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
			return err
		}
	}
	cfg.AccountToken = ""
	cfg.DeviceID = ""
	cfg.PrivateKey = ""
	cfg.DeviceIPv4 = netip.Prefix{}
	cfg.DeviceIPv6 = netip.Prefix{}
	cfg.DeviceName = ""
	cfg.AccountExpiry = time.Time{}
	return cfg.Save()
}

func devices(args []string) error {
	if len(args) < 1 {
		return usagef("usage: mvad devices <list|remove>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AccountToken == "" {
		return errors.New("not logged in")
	}
	switch args[0] {
	case "list":
		return devicesList(cfg, args[1:])
	case "remove":
		return devicesRemove(cfg, args[1:])
	default:
		return usagef("unknown devices subcommand %q", args[0])
	}
}

func devicesList(cfg *config.Config, args []string) error {
	if len(args) != 0 {
		return usagef("usage: mvad devices list")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	devs, err := mullvad.New().ListDevices(ctx, cfg.AccountToken)
	if err != nil {
		return err
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].Created.Before(devs[j].Created) })
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, d := range devs {
		pub := d.PublicKey.String()[:8]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, d.IPv4, d.Created.UTC().Format(time.RFC3339), pub)
	}
	return w.Flush()
}

func devicesRemove(cfg *config.Config, args []string) error {
	if len(args) != 1 {
		return usagef("usage: mvad devices remove <id>")
	}
	if args[0] == cfg.DeviceID {
		return errors.New("this is your current device; use mvad logout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := mullvad.New().RevokeDevice(ctx, cfg.AccountToken, args[0])
	if err != nil {
		var apiErr *mullvad.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	return nil
}

func rotateKey(args []string) error {
	if len(args) != 0 {
		return usagef("usage: mvad rotate-key")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AccountToken == "" || cfg.DeviceID == "" {
		return errors.New("not logged in")
	}
	s, err := status.Read(ifname)
	if err != nil && !errors.Is(err, status.ErrNotConnected) {
		return err
	}
	if s.Up {
		return errors.New("currently connected; run mvad disconnect first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := mullvad.New()
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return err
	}
	dev, err := c.RegisterDevice(ctx, cfg.AccountToken, priv.PublicKey())
	if err != nil {
		return err
	}
	oldID := cfg.DeviceID
	cfg.DeviceID = dev.ID
	cfg.PrivateKey = priv.String()
	cfg.DeviceIPv4 = dev.IPv4
	cfg.DeviceIPv6 = dev.IPv6
	cfg.DeviceName = dev.Name
	if err := cfg.Save(); err != nil {
		revokeErr := c.RevokeDevice(ctx, cfg.AccountToken, dev.ID)
		return errors.Join(err, revokeErr)
	}
	if err := c.RevokeDevice(ctx, cfg.AccountToken, oldID); err != nil {
		var apiErr *mullvad.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("revoke old device %s: %w (clean up with: mvad devices remove %s)", oldID, err, oldID)
	}
	return nil
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func matchesAny(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func listRelays(args []string) error {
	fs := flag.NewFlagSet("relays", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var country, city, provider stringList
	fs.Var(&country, "country", "filter by country (repeatable)")
	fs.Var(&city, "city", "filter by city (repeatable)")
	fs.Var(&provider, "provider", "filter by provider (repeatable)")
	owned := fs.String("owned", "", "filter by owned: true or false")
	protocol := fs.String("protocol", "", "filter by protocol: wireguard")
	bridges := fs.Bool("bridges", false, "list shadowsocks bridges instead of wireguard relays")
	refresh := fs.Bool("refresh", false, "force refetch from API, bypassing the 24h cache")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usagef("usage: mvad relays [--bridges] [--refresh] [--country C]... [--city C]... [--provider P]... [--owned true|false] [--protocol wireguard]")
	}
	var wantOwned, filterOwned bool
	switch *owned {
	case "":
	case "true":
		wantOwned, filterOwned = true, true
	case "false":
		wantOwned, filterOwned = false, true
	default:
		return usagef("--owned must be true or false")
	}
	if *protocol != "" && !strings.EqualFold(*protocol, "wireguard") {
		return usagef("--protocol: only wireguard is supported")
	}
	if *bridges {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		bs, _, err := mullvad.New().Bridges(ctx)
		if err != nil {
			return err
		}
		sort.Slice(bs, func(i, j int) bool { return bs[i].Hostname < bs[j].Hostname })
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, b := range bs {
			if !b.Active {
				continue
			}
			if !matchesAny(country, b.Country) || !matchesAny(city, b.City) || !matchesAny(provider, b.Provider) {
				continue
			}
			if filterOwned && b.Owned != wantOwned {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", b.Hostname, b.Country, b.City, b.IPv4, b.Provider)
		}
		return w.Flush()
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *refresh || len(cfg.RelayCache) == 0 || time.Since(cfg.RelaysFetchedAt) > 24*time.Hour {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		relays, err := mullvad.New().Relays(ctx)
		if err != nil {
			return err
		}
		data, err := json.Marshal(relays)
		if err != nil {
			return err
		}
		cfg.RelayCache = data
		cfg.RelaysFetchedAt = time.Now()
		if err := cfg.Save(); err != nil {
			return err
		}
	}
	var relays []mullvad.Relay
	if err := json.Unmarshal(cfg.RelayCache, &relays); err != nil {
		return err
	}
	sort.Slice(relays, func(i, j int) bool { return relays[i].Hostname < relays[j].Hostname })
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, r := range relays {
		if !r.Active {
			continue
		}
		if !matchesAny(country, r.Country) || !matchesAny(city, r.City) || !matchesAny(provider, r.Provider) {
			continue
		}
		if filterOwned && r.Owned != wantOwned {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Hostname, r.Country, r.City, r.IPv4, r.Provider)
	}
	return w.Flush()
}

func pickBridge(bs []mullvad.Bridge, name string) (mullvad.Bridge, error) {
	for _, b := range bs {
		if b.Hostname == name {
			return b, nil
		}
	}
	return mullvad.Bridge{}, fmt.Errorf("bridge %q not found", name)
}

func connect(args []string) (retErr error) {
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	var relay string
	defer func() {
		var uerr *usageError
		if errors.As(retErr, &uerr) {
			return
		}
		if retErr == nil {
			notify.Send("mvad", "connected to "+relay)
		} else {
			notify.Send("mvad: connect failed", retErr.Error())
		}
	}()
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	allowLAN := fs.Bool("allow-lan", false, "allow traffic to private LAN ranges")
	via := fs.String("via", "", "entry relay for multihop")
	transport := fs.String("transport", "wireguard", "transport: wireguard, tcp, or shadowsocks")
	tcpPort := fs.Uint("port", 5001, "udp2tcp gateway TCP port (80, 443, or 5001)")
	bridge := fs.String("bridge", "", "shadowsocks bridge hostname")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return usagef("usage: mvad connect [--allow-lan] [--via <entry>] [--transport wireguard|tcp|shadowsocks [--port 80|443|5001] [--bridge <host>]] <relay>")
	}
	useTCP, useSS := false, false
	switch *transport {
	case "wireguard":
	case "tcp":
		useTCP = true
	case "shadowsocks":
		useSS = true
	default:
		return usagef("--transport: must be wireguard, tcp, or shadowsocks")
	}
	if useTCP && !udp2tcpPorts[uint16(*tcpPort)] {
		return usagef("--port: must be 80, 443, or 5001")
	}
	if (useTCP || useSS) && *via != "" {
		return usagef("--transport %s does not support --via", *transport)
	}
	if useSS && *bridge == "" {
		return usagef("--transport shadowsocks requires --bridge <host>")
	}
	if !useSS && *bridge != "" {
		return usagef("--bridge requires --transport shadowsocks")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AccountToken == "" || cfg.DeviceID == "" || cfg.PrivateKey == "" {
		return errors.New("not logged in; run mvad login <token>")
	}
	if !cfg.DeviceIPv4.IsValid() {
		return errors.New("device address missing; run mvad login <token>")
	}
	if useSS {
		if _, err := exec.LookPath("ss-local"); err != nil {
			return errors.New("ss-local not found — install shadowsocks-libev")
		}
	}
	priv, err := wgtypes.ParseKey(cfg.PrivateKey)
	if err != nil {
		return err
	}
	exit, err := pickRelay(cfg, fs.Arg(0))
	if err != nil {
		return err
	}
	relay = exit.Hostname
	endpoint := netip.AddrPortFrom(exit.IPv4, wireguardPort)
	entryHost := ""
	if *via != "" {
		entry, err := pickRelay(cfg, *via)
		if err != nil {
			return err
		}
		if exit.MultihopPort == 0 {
			return fmt.Errorf("relay %q has no multihop port", exit.Hostname)
		}
		endpoint = netip.AddrPortFrom(entry.IPv4, exit.MultihopPort)
		entryHost = entry.Hostname
	}
	if useTCP {
		endpoint = netip.AddrPortFrom(exit.IPv4, uint16(*tcpPort))
	}
	var ssBridge mullvad.Bridge
	var ssEnd mullvad.ShadowsocksEndpoint
	if useSS {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		bridges, ss, err := mullvad.New().Bridges(ctx)
		if err != nil {
			return err
		}
		ssBridge, err = pickBridge(bridges, *bridge)
		if err != nil {
			return err
		}
		ssEnd = ss
		endpoint = netip.AddrPortFrom(ssBridge.IPv4, ss.Port)
	}
	wgEndpoint := endpoint
	if useTCP {
		wgEndpoint = netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), udp2tcpLocalPort)
	}
	if useSS {
		wgEndpoint = netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), shadowsocksLocalPort)
	}
	cfg.LastRelay = exit.Hostname
	cfg.LastEntryRelay = entryHost
	cfg.LastEndpoint = endpoint
	cfg.LastTransport = ""
	cfg.LastTransportPort = 0
	cfg.LastBridge = ""
	if useTCP {
		cfg.LastTransport = "tcp"
		cfg.LastTransportPort = uint16(*tcpPort)
	}
	if useSS {
		cfg.LastTransport = "shadowsocks"
		cfg.LastBridge = ssBridge.Hostname
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	wcfg := wg.Config{
		Name:       ifname,
		PrivateKey: priv,
		Address:    cfg.DeviceIPv4,
		Address6:   cfg.DeviceIPv6,
		PeerKey:    exit.PublicKey,
		Endpoint:   wgEndpoint,
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")},
		MTU:        1380,
	}
	if err := wg.Up(wcfg); err != nil {
		return err
	}
	if err := route.Set(ifname, endpoint.Addr()); err != nil {
		wg.Down(ifname)
		return err
	}
	if err := dns.Set(ifname, mullvadDNS); err != nil {
		route.Unset(ifname, endpoint.Addr())
		wg.Down(ifname)
		return err
	}
	fcfg := firewall.Config{
		Iface:    ifname,
		Endpoint: endpoint,
		DNS:      mullvadDNS,
		AllowLAN: *allowLAN,
		TCP:      useTCP,
	}
	if err := firewall.Up(fcfg); err != nil {
		dns.Restore(ifname)
		route.Unset(ifname, endpoint.Addr())
		wg.Down(ifname)
		return err
	}
	if useTCP {
		if err := udp2tcpStart(udp2tcpLocalPort, endpoint); err != nil {
			firewall.Down()
			dns.Restore(ifname)
			route.Unset(ifname, endpoint.Addr())
			wg.Down(ifname)
			return err
		}
	}
	if useSS {
		wgPeer := netip.AddrPortFrom(exit.IPv4, wireguardPort)
		if err := ssStart(shadowsocksLocalPort, ssBridge.IPv4, ssEnd, wgPeer); err != nil {
			firewall.Down()
			dns.Restore(ifname)
			route.Unset(ifname, endpoint.Addr())
			wg.Down(ifname)
			return err
		}
	}
	return nil
}

func reconnect(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	fs := flag.NewFlagSet("reconnect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	allowLAN := fs.Bool("allow-lan", false, "allow traffic to private LAN ranges")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usagef("usage: mvad reconnect [--allow-lan]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.LastRelay == "" {
		return errors.New("no previous connection to reconnect to")
	}
	var cargs []string
	if *allowLAN {
		cargs = append(cargs, "--allow-lan")
	}
	if cfg.LastEntryRelay != "" {
		cargs = append(cargs, "--via", cfg.LastEntryRelay)
	}
	if cfg.LastTransport == "tcp" {
		cargs = append(cargs, "--transport", "tcp", "--port", strconv.Itoa(int(cfg.LastTransportPort)))
	}
	if cfg.LastTransport == "shadowsocks" {
		cargs = append(cargs, "--transport", "shadowsocks", "--bridge", cfg.LastBridge)
	}
	cargs = append(cargs, cfg.LastRelay)
	if err := disconnect(nil); err != nil {
		return err
	}
	return connect(cargs)
}

func disconnect(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 0 {
		return usagef("usage: mvad disconnect")
	}
	var endpoint netip.Addr
	if cfg, err := config.Load(); err == nil {
		endpoint = cfg.LastEndpoint.Addr()
	}
	return errors.Join(ssStop(), udp2tcpStop(), firewall.Down(), dns.Restore(ifname), route.Unset(ifname, endpoint), wg.Down(ifname))
}

func lockdownCmd(args []string) error {
	if len(args) < 1 {
		return usagef("usage: mvad lockdown <on|off|refresh>")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "on":
		return lockdownOn(rest)
	case "off":
		return lockdownOff(rest)
	case "refresh":
		return lockdownRefresh(rest)
	default:
		return usagef("unknown lockdown subcommand %q", sub)
	}
}

func lockdownOn(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 0 {
		return usagef("usage: mvad lockdown on")
	}
	ips, err := loadRelayIPs()
	if err != nil {
		return err
	}
	return lockdown.On(ips)
}

func lockdownOff(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 0 {
		return usagef("usage: mvad lockdown off")
	}
	return lockdown.Off()
}

func lockdownRefresh(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 0 {
		return usagef("usage: mvad lockdown refresh")
	}
	ips, err := loadRelayIPs()
	if err != nil {
		return err
	}
	return lockdown.Refresh(ips)
}

func loadRelayIPs() ([]netip.Addr, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	ips, err := mullvad.RelayIPs(cfg.RelayCache)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("no cached relays; run mvad relays")
	}
	return ips, nil
}

func showStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "", "output format: json or waybar")
	refresh := fs.Bool("refresh", false, "refresh account expiry and device name from the API")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usagef("usage: mvad status [--format json|waybar] [--refresh]")
	}
	switch *format {
	case "", "json", "waybar":
	default:
		return usagef("--format: must be json or waybar")
	}
	cfg, err := config.Load()
	if err != nil {
		return &exitErr{code: 2, err: err}
	}
	if *refresh {
		if err := refreshAccount(cfg); err != nil {
			return &exitErr{code: 2, err: err}
		}
	}
	s, err := status.Read(ifname)
	if err != nil && !errors.Is(err, status.ErrNotConnected) {
		return &exitErr{code: 2, err: err}
	}
	s.Relay = cfg.LastRelay
	s.Entry = cfg.LastEntryRelay
	s.AccountExpiry = cfg.AccountExpiry
	s.DeviceName = cfg.DeviceName
	var out string
	switch *format {
	case "json":
		out, err = status.JSON(s)
	case "waybar":
		out, err = status.Waybar(s)
	default:
		out = status.Plain(s)
	}
	if err != nil {
		return &exitErr{code: 2, err: err}
	}
	fmt.Print(out)
	if !s.Up {
		return &exitErr{code: 1}
	}
	return nil
}

func refreshAccount(cfg *config.Config) error {
	if cfg.AccountToken == "" {
		return errors.New("not logged in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := mullvad.New()
	exp, err := c.AccountExpiry(ctx, cfg.AccountToken)
	if err != nil {
		return err
	}
	cfg.AccountExpiry = exp
	if cfg.DeviceID != "" {
		devs, err := c.ListDevices(ctx, cfg.AccountToken)
		if err != nil {
			return err
		}
		for _, d := range devs {
			if d.ID == cfg.DeviceID {
				cfg.DeviceName = d.Name
				break
			}
		}
	}
	return cfg.Save()
}

func udp2tcpShim(args []string) error {
	if len(args) != 2 {
		return errors.New("internal subcommand")
	}
	port, err := strconv.Atoi(args[0])
	if err != nil || port <= 0 || port > 65535 {
		return errors.New("internal subcommand")
	}
	if _, _, err := net.SplitHostPort(args[1]); err != nil {
		return errors.New("internal subcommand")
	}
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return err
	}
	defer udp.Close()
	tcp, err := net.Dial("tcp", args[1])
	if err != nil {
		return err
	}
	defer tcp.Close()
	return udp2tcp.Forward(udp, tcp)
}

func udp2tcpStart(localPort int, remote netip.AddrPort) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(udp2tcpPidFile), 0700); err != nil {
		return err
	}
	cmd := exec.Command(self, "__udp2tcp", strconv.Itoa(localPort), remote.String())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := []byte(strconv.Itoa(cmd.Process.Pid) + "\n")
	if err := os.WriteFile(udp2tcpPidFile, pid, 0600); err != nil {
		cmd.Process.Kill()
		return err
	}
	go cmd.Wait()
	return nil
}

func udp2tcpStop() error {
	data, err := os.ReadFile(udp2tcpPidFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		os.Remove(udp2tcpPidFile)
		return fmt.Errorf("udp2tcp pidfile: %w", err)
	}
	if udp2tcpShimAlive(pid) {
		p, _ := os.FindProcess(pid)
		_ = p.Signal(syscall.SIGTERM)
	}
	return os.Remove(udp2tcpPidFile)
}

// udp2tcpShimAlive reports whether pid still names our shim, guarding
// against PID recycling between connect and disconnect.
func udp2tcpShimAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "__udp2tcp")
}

func ssStart(localPort int, bridge netip.Addr, ss mullvad.ShadowsocksEndpoint, peer netip.AddrPort) error {
	bin, err := exec.LookPath("ss-local")
	if err != nil {
		return errors.New("ss-local not found — install shadowsocks-libev")
	}
	if err := os.MkdirAll(filepath.Dir(shadowsocksPidFile), 0700); err != nil {
		return err
	}
	cmd := exec.Command(bin,
		"-U",
		"-b", "127.0.0.1",
		"-l", strconv.Itoa(localPort),
		"-L", peer.String(),
		"-s", bridge.String(),
		"-p", strconv.Itoa(int(ss.Port)),
		"-m", ss.Cipher,
		"-k", ss.Password,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := []byte(strconv.Itoa(cmd.Process.Pid) + "\n")
	if err := os.WriteFile(shadowsocksPidFile, pid, 0600); err != nil {
		cmd.Process.Kill()
		return err
	}
	go cmd.Wait()
	return nil
}

func ssStop() error {
	data, err := os.ReadFile(shadowsocksPidFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		os.Remove(shadowsocksPidFile)
		return fmt.Errorf("shadowsocks pidfile: %w", err)
	}
	if ssAlive(pid) {
		p, _ := os.FindProcess(pid)
		_ = p.Signal(syscall.SIGTERM)
	}
	return os.Remove(shadowsocksPidFile)
}

// ssAlive reports whether pid still runs ss-local, guarding against
// PID recycling. /proc/<pid>/exe is kernel-set; cmdline is not.
func ssAlive(pid int) bool {
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return false
	}
	return filepath.Base(target) == "ss-local"
}

func pickRelay(cfg *config.Config, name string) (mullvad.Relay, error) {
	if len(cfg.RelayCache) == 0 {
		return mullvad.Relay{}, errors.New("no cached relays; run mvad relays")
	}
	var relays []mullvad.Relay
	if err := json.Unmarshal(cfg.RelayCache, &relays); err != nil {
		return mullvad.Relay{}, err
	}
	for _, r := range relays {
		if r.Hostname == name {
			return r, nil
		}
	}
	return mullvad.Relay{}, fmt.Errorf("relay %q not found", name)
}
