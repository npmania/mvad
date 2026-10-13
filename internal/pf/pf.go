// Package pf loads rules into anchors of the macOS packet filter and
// holds pf enabled while they are in use. Anchors under com.apple are
// evaluated through the anchor "com.apple/*" rule of the stock
// /etc/pf.conf.
package pf
