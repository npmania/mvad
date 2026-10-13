//go:build !linux && !darwin

package firewall

func up(Config) error { return ErrUnsupported }
func down() error     { return ErrUnsupported }
func installed() bool { return false }
