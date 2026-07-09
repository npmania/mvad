// Package split separates tagged traffic — processes in a cgroup v2
// plus source addresses in an nft set — from the rest of the system
// via a fwmark and a dedicated routing table. In full-tunnel mode the
// tagged traffic bypasses the tunnel; with Config.Split only the
// tagged traffic uses it.
package split

import (
	"errors"
	"net/netip"
)

const (
	cgroupRoot = "/sys/fs/cgroup"
	cgroupName = "mvad-split"
	cgroupDir  = cgroupRoot + "/" + cgroupName
	stateFile  = "/run/mvad/split-route.json"
	tableName  = "mvad-split"
	setName    = "net"
	routeTable = 60
	dnsPri     = 97
	mainPri    = 98
	rulePri    = 99
	fwmark     = 0xca6c
)

var (
	ErrUnsupported = errors.New("split: unsupported platform")
	ErrUnavailable = errors.New("split-tunnel inactive")
)

type Config struct {
	Split    bool       // route only tagged traffic through Iface
	Iface    string     // tunnel interface (split mode)
	Gateway  netip.Addr // plain gateway (full-tunnel mode)
	Gateway6 netip.Addr
	Dev      string         // plain device (full-tunnel mode)
	DNS      []netip.Addr   // in-tunnel resolvers
	HasV6    bool           // tunnel carries IPv6
	Nets     []netip.Prefix // source addresses to tag
}

func Up(c Config) error               { return up(c) }
func Down() error                     { return down() }
func AddPID(pid int) error            { return addPID(pid) }
func RmPID(pid int) error             { return rmPID(pid) }
func ListPIDs() ([]int, error)        { return listPIDs() }
func SetNets(ps []netip.Prefix) error { return setNets(ps) }
func Clear() error                    { return clear() }
func Available() bool                 { return available() }
func SplitMode() bool                 { return splitMode() }

const (
	CgroupDir = cgroupDir
	FWMark    = fwmark
)
