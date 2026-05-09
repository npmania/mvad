//go:build !linux

package route

func set(string) error   { return ErrUnsupported }
func unset(string) error { return ErrUnsupported }
