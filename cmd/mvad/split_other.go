//go:build !linux

package main

import "errors"

func runCmd([]string) error   { return errors.New("run: unsupported platform") }
func splitCmd([]string) error { return errors.New("split: unsupported platform") }
func escapeSplitCgroup()      {}
