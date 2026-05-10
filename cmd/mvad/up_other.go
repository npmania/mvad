//go:build !linux

package main

import "errors"

func up(args []string) error {
	return errors.New("up: unsupported platform")
}
