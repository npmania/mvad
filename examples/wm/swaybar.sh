#!/bin/sh
# sway: bar { status_command = /path/to/swaybar.sh }
# Emits the i3bar protocol stream. Requires jq.
echo '{"version":1}'
echo '['
echo '[]'
while :; do
	block=$(mvad status --format=waybar | jq -c '[{full_text:.text, color:(if .class=="connected" then "#5fff5f" else "#ff5f5f" end)}]')
	printf ',%s\n' "$block"
	sleep 2
done
