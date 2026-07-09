# mvad

A small Mullvad VPN client for Linux. Speaks the Mullvad API and the
kernel WireGuard driver directly; no daemon.

Two commands:

- `mvad` — CLI: account, relays, connect, status, kill-switch, split-tunnel.
- `mvad-gui` — Gio GUI with tray; thin wrapper over `mvad`.

Features:

- WireGuard via wgctrl (kernel module required).
- Multihop, udp2tcp, and shadowsocks-bridge transports.
- nftables kill-switch; persistent lockdown that survives reboots.
- Split-tunnel by cgroup v2, IP, docker container, or compose service
  (`mvad run`, `mvad split`).
- Split mode for servers: the system stays on the plain network and
  only the split set goes through Mullvad (`connect --split`).
- Status as plain text, JSON, or Waybar.

## Build

	go build ./cmd/...

The GUI links against Gio and needs X11 or Wayland dev headers.

## Usage

	mvad signup                   # or: mvad login <token>
	mvad relays
	sudo mvad connect se-got-wg-001
	mvad status
	sudo mvad disconnect

The split set — pids in the mvad-split cgroup plus forwarded source
addresses (containers, VMs) added with `mvad split
add-ip/add-docker/add-compose` — is the traffic separated from the
rest. A plain `connect` tunnels everything and the
split set bypasses the tunnel. `connect --split` inverts that: the
system stays on the plain network and only the split set is tunneled,
with its port-53 DNS rewritten to the in-tunnel resolver and a
fail-closed route if the tunnel drops.

	sudo mvad connect --split se-got-wg-001
	sudo mvad run -- curl https://am.i.mullvad.net/connected
	sudo mvad split add-docker grafana
	sudo mvad split add-compose myapp         # whole project
	sudo mvad split add-compose myapp worker  # one service

Split-mode fine print: destinations with specific routes (the LAN,
docker networks) stay direct, as do lookups through a loopback stub
like systemd-resolved or docker's embedded DNS. add-docker/add-compose
record the container's addresses at add time; after an address change,
re-add and prune the old entry with `split list` and `rm-ip`.

`mvad help` lists every command; each has `--help`.

Unit files for systemd, OpenRC, and runit, plus a `.desktop` entry
and a polkit policy, live in `examples/`.
