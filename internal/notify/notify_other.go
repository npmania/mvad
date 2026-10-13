//go:build !linux && !darwin

package notify

func Send(title, body string) error { return nil }
