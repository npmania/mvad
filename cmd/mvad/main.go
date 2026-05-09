package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"runtime/debug"
	"sort"
	"text/tabwriter"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/npmania/mvad/internal/config"
	"github.com/npmania/mvad/internal/dns"
	"github.com/npmania/mvad/internal/mullvad"
	"github.com/npmania/mvad/internal/route"
	"github.com/npmania/mvad/internal/status"
	"github.com/npmania/mvad/internal/wg"
)

const ifname = "mvad-wg0"

var mullvadDNS = []netip.Addr{netip.MustParseAddr("10.64.0.1")}

const usageText = `usage: mvad <command> [arguments]

The commands are:

	login       store a Mullvad account number
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
	case "login", "relays":
		if os.Geteuid() == 0 {
			fmt.Fprintln(os.Stderr, "mvad: warning: running as root; this writes config to root's home, breaking subsequent unprivileged calls")
		}
	}
	var err error
	switch cmd {
	case "login":
		err = login(args)
	case "relays":
		err = listRelays(args)
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
	if len(args) != 1 {
		return usagef("usage: mvad login <token>")
	}
	token := args[0]
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

func listRelays(args []string) error {
	if len(args) != 0 {
		return usagef("usage: mvad relays")
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Hostname, r.Country, r.City, r.IPv4, r.Provider)
	}
	return w.Flush()
}

func connect(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("this command needs root; rerun with sudo")
	}
	if len(args) != 1 {
		return usagef("usage: mvad connect <relay>")
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
	relay, err := pickRelay(cfg, args[0])
	if err != nil {
		return err
	}
	cfg.LastRelay = relay.Hostname
	cfg.LastEndpoint = netip.AddrPortFrom(relay.IPv4, 51820)
	if err := cfg.Save(); err != nil {
		return err
	}
	wcfg := wg.Config{
		Name:       ifname,
		PrivateKey: priv,
		Address:    cfg.DeviceIPv4,
		Address6:   cfg.DeviceIPv6,
		PeerKey:    relay.PublicKey,
		Endpoint:   netip.AddrPortFrom(relay.IPv4, 51820),
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")},
		MTU:        1380,
	}
	if err := wg.Up(wcfg); err != nil {
		return err
	}
	if err := route.Set(ifname, relay.IPv4); err != nil {
		wg.Down(ifname)
		return err
	}
	if err := dns.Set(mullvadDNS); err != nil {
		route.Unset(ifname, relay.IPv4)
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
	return errors.Join(dns.Restore(), route.Unset(ifname, endpoint), wg.Down(ifname))
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
