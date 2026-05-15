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
- cgroup v2 split-tunnel (`mvad run`, `mvad split`).
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

`mvad help` lists every command; each has `--help`.

Unit files for systemd, OpenRC, and runit, plus a `.desktop` entry
and a polkit policy, live in `examples/`.

## License

MIT.
