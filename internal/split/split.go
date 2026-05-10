// Package split routes traffic from a tagged cgroup v2 outside the
// tunnel via a fwmark and a separate routing table.
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
	routeTable = 60
	rulePri    = 99
	fwmark     = 0xca6c
)

var (
	ErrUnsupported = errors.New("split: unsupported platform")
	ErrUnavailable = errors.New("split-tunnel inactive")
)

func Up(gw netip.Addr, dev string) error { return up(gw, dev) }
func Down() error                        { return down() }
func AddPID(pid int) error               { return addPID(pid) }
func ListPIDs() ([]int, error)           { return listPIDs() }
func Clear() error                       { return clear() }
func Available() bool                    { return available() }

const CgroupDir = cgroupDir
