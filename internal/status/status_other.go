//go:build !linux && !darwin

package status

func read(string) (Snapshot, error) { return Snapshot{}, ErrUnsupported }
