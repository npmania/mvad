//go:build !linux

package main

import (
	"errors"
	"net/netip"

	"github.com/npmania/mvad/internal/config"
)

func runCmd([]string) error    { return errors.New("run: unsupported platform") }
func splitCmd([]string) error  { return errors.New("split: unsupported platform") }
func escapeSplitCgroup()       {}
func markSocket(uintptr) error { return nil }

func resolveSplitNets(*config.Config) ([]netip.Prefix, []error) { return nil, nil }
