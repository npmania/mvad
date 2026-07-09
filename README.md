# mvad

A small Mullvad VPN client for Linux. Speaks the Mullvad API and the
kernel WireGuard driver directly; no daemon. The interface, routes,
firewall, and DNS are the state: every command runs and exits, like
ip(8).

Two commands:

- `mvad` — CLI: account, relays, connect, status, kill-switch, split-tunnel.
- `mvad-gui` — Gio GUI with tray; thin wrapper over `mvad`.

## Requirements

- Linux with the wireguard module, nftables, iproute2, and cgroup v2.
- A Mullvad account: `mvad signup` creates one; add credit at
  mullvad.net/account.
- Root for anything that touches the network. Use sudo, or install
  examples/mvad.policy so the GUI can escalate through pkexec.
- shadowsocks-libev, only for `--transport shadowsocks`.

## Build

	go build ./cmd/...

The GUI links against Gio and needs X11 or Wayland dev headers; the
CLI alone (`go build ./cmd/mvad`) needs none.

## Usage

	mvad signup                   # or: mvad login <token>
	mvad relays --country se
	sudo mvad connect se-got-wg-001
	mvad status
	sudo mvad disconnect

`mvad help` lists every command; each has `--help`.

### Connecting

The relay argument is an exact hostname (`jp-osa-wg-001`) or a prefix
(`jp`, `jp-osa`) picking a random active relay in that scope.

`connect` replaces the default route with the tunnel, points DNS at
the in-tunnel resolver, and installs an nftables kill-switch that
drops anything not headed for the relay. `--allow-lan` opens the
private ranges. `reconnect` re-picks from the last query, avoiding the
relay it is leaving; `up` stays in the foreground and reconnects when
the default route changes (suspend, Wi-Fi roam) — see
examples/mvad-up@.service.

Multihop and censored networks:

	sudo mvad connect --via se-sto-wg-001 se-got-wg-001
	sudo mvad connect --transport tcp --port 443 se-got-wg-001
	sudo mvad connect --transport shadowsocks --bridge <host> se-got-wg-001
	mvad relays --bridges

### Lockdown

The connect kill-switch dies with the connection. `lockdown on`
installs a second ruleset that survives reboots (given a boot unit
from examples/) and lets only relay traffic out, so the machine cannot
leak between boot and connect. Run `lockdown refresh` after `mvad
relays --refresh` to pick up new relay addresses.

### Split tunneling

The split set — pids in the mvad-split cgroup plus forwarded source
addresses (containers, VMs) added with `mvad split
add-ip/add-docker/add-compose` — is the traffic separated from the
rest. A plain `connect` tunnels everything and the split set bypasses
the tunnel. `connect --split` inverts that: the system stays on the
plain network and only the split set is tunneled, with its port-53 DNS
rewritten to the in-tunnel resolver and a fail-closed route if the
tunnel drops.

	sudo mvad connect --split se-got-wg-001
	sudo mvad run -- curl https://am.i.mullvad.net/connected
	sudo mvad split add-docker grafana
	sudo mvad split add-compose myapp         # whole project
	sudo mvad split add-compose myapp worker  # one service

docker and compose entries are recorded by name and resolved to
addresses at every connect; after containers restart with new
addresses, run `sudo mvad split refresh` to reconcile the live set (a
deploy hook or timer does fine — the connection itself is untouched).
Until that refresh runs, the new addresses are not in the tunnel.

Split-mode fine print: destinations with specific routes (the LAN,
docker networks) stay direct, as do lookups through a loopback stub
like systemd-resolved or docker's embedded DNS. Marked traffic that
would leave on any other interface is dropped, so a rogue route can't
pull the split set out of the tunnel.

### Failover

Relays die, and a dead relay makes no noise — the interface stays up
while traffic goes nowhere. `sudo mvad check` sends a DNS query to the
in-tunnel resolver through the tunnel (through the split routing in
split mode): exit 0 when it answers, 1 when the tunnel is dead, 3 when
there is no tunnel. examples/mvad-check.timer runs check every minute
and reconnects only on a dead tunnel, so a deliberate disconnect stays
down. reconnect re-picks within the original query's scope avoiding
both hops it is leaving: connect with `jp-osa` and a dead relay is
replaced by another Osaka relay. An exact-hostname session has nowhere
to fail over to and just redials. The probe holds no lock, so in rare
timing a failover can replace a relay you picked manually seconds
earlier; the next tick settles it.

### Status

	mvad status                    # plain text; exit 1 when down
	mvad status --format=json
	mvad status --format=waybar

examples/wm/ has snippets for waybar, i3blocks, polybar, dwmblocks,
and swaybar.

## Files

Everything mvad touches, exhaustively:

- `~/.config/mvad/config.json` — token, device key, relay cache,
  options (0600; written through sudo/pkexec it stays owned by the
  invoking user).
- `/run/mvad/` — lock files, shim pidfiles, split state. tmpfs; the
  lock files linger between sessions and vanish at reboot.
- `/var/lib/mvad/` — the persistent kill-switch ruleset, only while
  `lockdown on`; removed by `lockdown off`.
- `/sys/fs/cgroup/mvad-split` — the split cgroup; removed at
  disconnect once no member processes remain.
- Kernel state — the wg interface, nft tables (`inet mvad`,
  `ip/ip6 mvad-split`, `inet mvad-lockdown`), ip rules 97–99, and
  routing table 60 — all removed by `sudo mvad disconnect`
  (`lockdown off` for the lockdown table), even after a crash.
- The sysctl `net.ipv4.conf.all.src_valid_mark` is set to 1 and, as
  with wg-quick, left set until reboot: reverting it could break
  another fwmark-routed tunnel.

To purge mvad completely: `sudo mvad disconnect`, `sudo mvad lockdown
off`, `mvad logout`, then delete `~/.config/mvad` and whatever you
installed from examples/.

## examples/

Unit files for systemd, OpenRC, runit, and rc.local covering the
tunnel (mvad-up@), split mode (mvad-split@), lockdown, and the
failover and split-refresh timers; a .desktop entry, polkit policy,
tray icon, and a tmpfiles.d entry.
