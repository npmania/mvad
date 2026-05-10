//go:build !linux

package notify

func Send(title, body string) error { return nil }
