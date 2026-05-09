package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/npmania/mvad/internal/config"
	"github.com/npmania/mvad/internal/dns"
	"github.com/npmania/mvad/internal/firewall"
	"github.com/npmania/mvad/internal/mullvad"
	"github.com/npmania/mvad/internal/route"
	"github.com/npmania/mvad/internal/status"
	"github.com/npmania/mvad/internal/wg"
)

const (
	ifname        = "mvad-wg0"
	wireguardPort = 51820
)

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
	status      print connection status
	version     print version
`

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

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
	case "status":
		err = showStatus(args)
	case "version":
		fmt.Println("mvad", versionString())
		return
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
	if _, err := c.AccountExpiry(ctx, token); err != nil {
		return err
	}
	if *keyFlag != "" {
		return loginImport(ctx, c, token, *keyFlag)
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
	if err := cfg.Save(); err != nil {
		revokeErr := c.RevokeDevice(ctx, token, dev.ID)
		return errors.Join(err, revokeErr)
	}
	return nil
}

func loginImport(ctx context.Context, c *mullvad.Client, token, keyStr string) error {
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
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usagef("usage: mvad relays [--country C]... [--city C]... [--provider P]... [--owned true|false] [--protocol wireguard]")
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.RelayCache) == 0 || time.Since(cfg.RelaysFetchedAt) > 24*time.Hour {
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
		if !matchesAny(country, r.Country) {
			continue
		}
		if !matchesAny(city, r.City) {
			continue
		}
		if !matchesAny(provider, r.Provider) {
			continue
		}
		if filterOwned && r.Owned != wantOwned {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Hostname, r.Country, r.City, r.IPv4, r.Provider)
	}
	return w.Flush()
}

func connect(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	allowLAN := fs.Bool("allow-lan", false, "allow traffic to private LAN ranges")
	via := fs.String("via", "", "entry relay for multihop")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return usagef("usage: mvad connect [--allow-lan] [--via <entry>] <relay>")
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
	priv, err := wgtypes.ParseKey(cfg.PrivateKey)
	if err != nil {
		return err
	}
	exit, err := pickRelay(cfg, fs.Arg(0))
	if err != nil {
		return err
	}
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
	cfg.LastRelay = exit.Hostname
	cfg.LastEntryRelay = entryHost
	cfg.LastEndpoint = endpoint
	if err := cfg.Save(); err != nil {
		return err
	}
	wcfg := wg.Config{
		Name:       ifname,
		PrivateKey: priv,
		Address:    cfg.DeviceIPv4,
		Address6:   cfg.DeviceIPv6,
		PeerKey:    exit.PublicKey,
		Endpoint:   endpoint,
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
	}
	if err := firewall.Up(fcfg); err != nil {
		dns.Restore(ifname)
		route.Unset(ifname, endpoint.Addr())
		wg.Down(ifname)
		return err
	}
	return nil
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
	return errors.Join(firewall.Down(), dns.Restore(ifname), route.Unset(ifname, endpoint), wg.Down(ifname))
}

func showStatus(args []string) error {
	if len(args) != 0 {
		return usagef("usage: mvad status")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	s, err := status.Read(ifname)
	if errors.Is(err, status.ErrNotConnected) {
		fmt.Print(status.Plain(s))
		return nil
	}
	if err != nil {
		return err
	}
	s.Relay = cfg.LastRelay
	s.Entry = cfg.LastEntryRelay
	fmt.Print(status.Plain(s))
	return nil
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
