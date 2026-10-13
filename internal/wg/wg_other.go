//go:build !linux && !darwin

package wg

const iface = "mvad-wg0"

func serve(string) error         { return ErrUnsupported }
func up(Config) error            { return ErrUnsupported }
func down(string) error          { return ErrUnsupported }
func read(string) (State, error) { return State{}, ErrUnsupported }
