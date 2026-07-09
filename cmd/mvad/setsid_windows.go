//go:build windows

package main

import "syscall"

func setsidAttr() *syscall.SysProcAttr { return nil }
