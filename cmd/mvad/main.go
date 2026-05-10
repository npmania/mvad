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
	"github.com/npmania/mvad/internal/lock"
	"github.com/npmania/mvad/internal/lockdown"
	"github.com/npmania/mvad/internal/mullvad"
	"github.com/npmania/mvad/internal/notify"
	"github.com/npmania/mvad/internal/route"
	"github.com/npmania/mvad/internal/split"
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

	signup      create a new Mullvad account
	login       log in to a Mullvad account
	logout      revoke this device and clear stored credentials
	devices     list or remove devices on this account
	rotate-key  generate a new WireGuard key for this device
	relays      list relays
	connect     connect to a relay
	disconnect  disconnect
	reconnect   reconnect to the last relay
	up          stay connected; reconnect on default-route changes
	lockdown    install or remove the persistent kill-switch
	run         run a command outside the tunnel via cgroup v2
	split       manage the split-tunnel cgroup
	status      print connection status
	version     print version

Run 'mvad <command> --help' for command-specific options.
`

const (
	usageSignup          = "usage: mvad signup"
	usageLogin           = "usage: mvad login [--key <base64-privkey>] [<token>]"
	usageLogout          = "usage: mvad logout"
	usageDevices         = "usage: mvad devices <list|remove>"
	usageDevicesList     = "usage: mvad devices list"
	usageDevicesRemove   = "usage: mvad devices remove <id>"
	usageRotateKey       = "usage: mvad rotate-key"
	usageRelays          = "usage: mvad relays [--bridges] [--refresh] [--json] [--country C]... [--city C]... [--provider P]... [--owned true|false] [--protocol wireguard]"
	usageConnect         = "usage: mvad connect [--allow-lan] [--via <entry>] [--transport wireguard|tcp|shadowsocks [--port 80|443|5001] [--bridge <host>]] <relay>"
	usageReconnect       = "usage: mvad reconnect [--allow-lan]"
	usageUp              = "usage: mvad up [--allow-lan] [--via <entry>] [--transport wireguard|tcp|shadowsocks [--port 80|443|5001] [--bridge <host>]] <relay>"
	usageDisconnect      = "usage: mvad disconnect"
	usageStatus          = "usage: mvad status [--format json|waybar] [--refresh]"
	usageLockdown        = "usage: mvad lockdown <on|off|refresh>"
	usageLockdownOn      = "usage: mvad lockdown on"
	usageLockdownOff     = "usage: mvad lockdown off"
	usageLockdownRefresh = "usage: mvad lockdown refresh"
	usageRun             = "usage: mvad run [--] <command> [args...]"
	usageSplit           = "usage: mvad split <add-pid|list|clear>"
	usageSplitAddPID     = "usage: mvad split add-pid <pid>"
	usageSplitList       = "usage: mvad split list"
	usageSplitClear      = "usage: mvad split clear"
)

func wantHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "--h", "-help", "--help":
			return true
		}
	}
	return false
}

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
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "help", "-h", "--h", "-help", "--help":
			fmt.Print(usageText)
			return
		}
	}
	flag.Usage = func() { fmt.Fprint(os.Stderr, usageText) }
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	args := flag.Args()[1:]
	cmd := flag.Arg(0)
	switch cmd {
	case "signup", "login", "logout", "relays", "rotate-key":
		if os.Geteuid() == 0 {
			fmt.Fprintln(os.Stderr, "mvad: warning: running as root; this writes config to root's home, breaking subsequent unprivileged calls")
		}
	}
	var err error
	switch cmd {
	case "signup":
		err = signup(args)
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
	case "up":
		err = up(args)
	case "lockdown":
		err = lockdownCmd(args)
	case "run":
		err = runCmd(args)
	case "split":
		err = splitCmd(args)
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

func signup(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageSignup)
		return nil
	}
	if len(args) != 0 {
		return usagef(usageSignup)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.DeviceID != "" {
		who := cfg.DeviceName
		if who == "" {
			who = cfg.DeviceID
		}
		return fmt.Errorf("already logged in as %s; run mvad logout first", who)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	num, err := mullvad.New().CreateAccount(ctx)
	if err != nil {
		return fmt.Errorf("create account: %w", err)
	}
	cfg.AccountToken = num
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Println(num)
	return nil
}

func login(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	keyFlag := fs.String("key", "", "base64 wireguard private key of an existing device to import")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(usageLogin)
			return nil
		}
		return usagef(usageLogin)
	}
	if fs.NArg() > 1 {
		return usagef(usageLogin)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var token string
	switch {
	case fs.NArg() == 1:
		token = fs.Arg(0)
	case cfg.AccountToken != "":
		token = cfg.AccountToken
	default:
		return usagef(usageLogin)
	}
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
		return loginImport(ctx, c, cfg, token, *keyFlag, expiry)
	}
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return err
	}
	dev, err := c.RegisterDevice(ctx, token, priv.PublicKey())
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

func loginImport(ctx context.Context, c *mullvad.Client, cfg *config.Config, token, keyStr string, expiry time.Time) error {
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
	if wantHelp(args) {
		fmt.Println(usageLogout)
		return nil
	}
	if len(args) != 0 {
		return usagef(usageLogout)
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
	if len(args) == 0 {
		return usagef(usageDevices)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "--h", "-help", "--help":
		fmt.Println(usageDevices)
		return nil
	}
	switch sub {
	case "list":
		if wantHelp(rest) {
			fmt.Println(usageDevicesList)
			return nil
		}
	case "remove":
		if wantHelp(rest) {
			fmt.Println(usageDevicesRemove)
			return nil
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AccountToken == "" {
		return errors.New("not logged in")
	}
	switch sub {
	case "list":
		return devicesList(cfg, rest)
	case "remove":
		return devicesRemove(cfg, rest)
	default:
		return usagef("unknown devices subcommand %q", sub)
	}
}

func devicesList(cfg *config.Config, args []string) error {
	if len(args) != 0 {
		return usagef(usageDevicesList)
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
		return usagef(usageDevicesRemove)
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
	if wantHelp(args) {
		fmt.Println(usageRotateKey)
		return nil
	}
	if len(args) != 0 {
		return usagef(usageRotateKey)
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
	asJSON := fs.Bool("json", false, "output as JSON array")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(usageRelays)
			return nil
		}
		return usagef(usageRelays)
	}
	if fs.NArg() != 0 {
		return usagef(usageRelays)
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
		var keep []mullvad.Bridge
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
			keep = append(keep, b)
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(keep)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, b := range keep {
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
	var keep []mullvad.Relay
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
		keep = append(keep, r)
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(keep)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, r := range keep {
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

type connectOpts struct {
	relay     string
	via       string
	allowLAN  bool
	transport string
	tcpPort   uint16
	bridge    string
}

func parseConnectOpts(args []string, usage string) (connectOpts, error) {
	var o connectOpts
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.allowLAN, "allow-lan", false, "allow traffic to private LAN ranges")
	fs.StringVar(&o.via, "via", "", "entry relay for multihop")
	fs.StringVar(&o.transport, "transport", "wireguard", "transport: wireguard, tcp, or shadowsocks")
	tcpPort := fs.Uint("port", 5001, "udp2tcp gateway TCP port (80, 443, or 5001)")
	fs.StringVar(&o.bridge, "bridge", "", "shadowsocks bridge hostname")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, flag.ErrHelp
		}
		return o, usagef("%s", usage)
	}
	if fs.NArg() != 1 {
		return o, usagef("%s", usage)
	}
	o.relay = fs.Arg(0)
	o.tcpPort = uint16(*tcpPort)
	switch o.transport {
	case "wireguard", "tcp", "shadowsocks":
	default:
		return o, usagef("--transport: must be wireguard, tcp, or shadowsocks")
	}
	if o.transport == "tcp" && !udp2tcpPorts[o.tcpPort] {
		return o, usagef("--port: must be 80, 443, or 5001")
	}
	if o.transport != "wireguard" && o.via != "" {
		return o, usagef("--transport %s does not support --via", o.transport)
	}
	if o.transport == "shadowsocks" && o.bridge == "" {
		return o, usagef("--transport shadowsocks requires --bridge <host>")
	}
	if o.transport != "shadowsocks" && o.bridge != "" {
		return o, usagef("--bridge requires --transport shadowsocks")
	}
	return o, nil
}

func connect(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageConnect)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
	opts, err := parseConnectOpts(args, usageConnect)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(usageConnect)
			return nil
		}
		return err
	}
	return doConnect(opts)
}

func doConnect(opts connectOpts) (retErr error) {
	defer func() {
		var uerr *usageError
		if errors.As(retErr, &uerr) {
			return
		}
		if retErr == nil {
			notify.Send("mvad", "connected to "+opts.relay)
		} else {
			notify.Send("mvad: connect failed", retErr.Error())
		}
	}()
	useTCP := opts.transport == "tcp"
	useSS := opts.transport == "shadowsocks"
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
		return fmt.Errorf("parse stored private key: %w (run mvad rotate-key to regenerate)", err)
	}
	exit, err := pickRelay(cfg, opts.relay)
	if err != nil {
		return fmt.Errorf("pick exit relay: %w (run mvad relays --refresh to update the cache)", err)
	}
	endpoint := netip.AddrPortFrom(exit.IPv4, wireguardPort)
	entryHost := ""
	if opts.via != "" {
		entry, err := pickRelay(cfg, opts.via)
		if err != nil {
			return fmt.Errorf("pick entry relay: %w (run mvad relays --refresh to update the cache)", err)
		}
		if exit.MultihopPort == 0 {
			return fmt.Errorf("relay %q has no multihop port", exit.Hostname)
		}
		endpoint = netip.AddrPortFrom(entry.IPv4, exit.MultihopPort)
		entryHost = entry.Hostname
	}
	if useTCP {
		endpoint = netip.AddrPortFrom(exit.IPv4, opts.tcpPort)
	}
	var ssBridge mullvad.Bridge
	var ssEnd mullvad.ShadowsocksEndpoint
	if useSS {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		bridges, ss, err := mullvad.New().Bridges(ctx)
		if err != nil {
			return fmt.Errorf("fetch shadowsocks bridges: %w", err)
		}
		ssBridge, err = pickBridge(bridges, opts.bridge)
		if err != nil {
			return fmt.Errorf("pick bridge: %w", err)
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
		cfg.LastTransportPort = opts.tcpPort
	}
	if useSS {
		cfg.LastTransport = "shadowsocks"
		cfg.LastBridge = ssBridge.Hostname
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
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
		return fmt.Errorf("configure wireguard interface: %w (is the wireguard kernel module loaded?)", err)
	}
	gw, dev, gwErr := route.Default()
	if err := route.Set(ifname, endpoint.Addr()); err != nil {
		wg.Down(ifname)
		return fmt.Errorf("set default route: %w", err)
	}
	if err := dns.Set(ifname, mullvadDNS); err != nil {
		route.Unset(ifname, endpoint.Addr())
		wg.Down(ifname)
		return fmt.Errorf("configure dns: %w", err)
	}
	fcfg := firewall.Config{
		Iface:    ifname,
		Endpoint: endpoint,
		DNS:      mullvadDNS,
		AllowLAN: opts.allowLAN,
		TCP:      useTCP,
	}
	if err := firewall.Up(fcfg); err != nil {
		dns.Restore(ifname)
		route.Unset(ifname, endpoint.Addr())
		wg.Down(ifname)
		return fmt.Errorf("install nft kill-switch: %w (run nft list ruleset as root to inspect)", err)
	}
	if useTCP {
		if err := udp2tcpStart(udp2tcpLocalPort, endpoint); err != nil {
			firewall.Down()
			dns.Restore(ifname)
			route.Unset(ifname, endpoint.Addr())
			wg.Down(ifname)
			return fmt.Errorf("start udp2tcp shim: %w", err)
		}
	}
	if useSS {
		wgPeer := netip.AddrPortFrom(exit.IPv4, wireguardPort)
		if err := ssStart(shadowsocksLocalPort, ssBridge.IPv4, ssEnd, wgPeer); err != nil {
			firewall.Down()
			dns.Restore(ifname)
			route.Unset(ifname, endpoint.Addr())
			wg.Down(ifname)
			return fmt.Errorf("start ss-local: %w", err)
		}
	}
	if gwErr != nil {
		fmt.Fprintf(os.Stderr, "mvad: split-tunnel setup skipped: %v; running without split-tunnel\n", gwErr)
	} else if err := split.Up(gw, dev); err != nil {
		fmt.Fprintf(os.Stderr, "mvad: split-tunnel setup failed: %v; running without split-tunnel\n", err)
	}
	return nil
}

func reconnect(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageReconnect)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
	fs := flag.NewFlagSet("reconnect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	allowLAN := fs.Bool("allow-lan", false, "allow traffic to private LAN ranges")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(usageReconnect)
			return nil
		}
		return usagef(usageReconnect)
	}
	if fs.NArg() != 0 {
		return usagef(usageReconnect)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.LastRelay == "" {
		return errors.New("no previous connection to reconnect to")
	}
	opts := connectOpts{
		relay:     cfg.LastRelay,
		via:       cfg.LastEntryRelay,
		allowLAN:  *allowLAN,
		transport: "wireguard",
	}
	switch cfg.LastTransport {
	case "tcp":
		opts.transport = "tcp"
		opts.tcpPort = cfg.LastTransportPort
	case "shadowsocks":
		opts.transport = "shadowsocks"
		opts.bridge = cfg.LastBridge
	}
	if err := doDisconnect(); err != nil {
		return err
	}
	return doConnect(opts)
}

func disconnect(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageDisconnect)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 0 {
		return usagef(usageDisconnect)
	}
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
	return doDisconnect()
}

func doDisconnect() error {
	var endpoint netip.Addr
	if cfg, err := config.Load(); err == nil {
		endpoint = cfg.LastEndpoint.Addr()
	}
	if err := split.Down(); err != nil {
		fmt.Fprintf(os.Stderr, "mvad: split-tunnel teardown: %v\n", err)
	}
	return errors.Join(ssStop(), udp2tcpStop(), firewall.Down(), dns.Restore(ifname), route.Unset(ifname, endpoint), wg.Down(ifname))
}

func lockdownCmd(args []string) error {
	if len(args) == 0 {
		return usagef(usageLockdown)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "--h", "-help", "--help":
		fmt.Println(usageLockdown)
		return nil
	}
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
	if wantHelp(args) {
		fmt.Println(usageLockdownOn)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 0 {
		return usagef(usageLockdownOn)
	}
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
	ips, err := loadRelayIPs()
	if err != nil {
		return err
	}
	return lockdown.On(ips)
}

func lockdownOff(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageLockdownOff)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 0 {
		return usagef(usageLockdownOff)
	}
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
	return lockdown.Off()
}

func lockdownRefresh(args []string) error {
	if wantHelp(args) {
		fmt.Println(usageLockdownRefresh)
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 0 {
		return usagef(usageLockdownRefresh)
	}
	release, err := lock.AcquireRoot()
	if err != nil {
		return err
	}
	defer release()
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
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(usageStatus)
			return nil
		}
		return usagef(usageStatus)
	}
	if fs.NArg() != 0 {
		return usagef(usageStatus)
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
