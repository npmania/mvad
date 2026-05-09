//go:build !linux

package firewall

func up(Config) error { return ErrUnsupported }
func down() error     { return ErrUnsupported }
