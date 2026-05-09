//go:build !linux

package wg

func up(Config) error            { return ErrUnsupported }
func down(string) error          { return ErrUnsupported }
func read(string) (State, error) { return State{}, ErrUnsupported }
