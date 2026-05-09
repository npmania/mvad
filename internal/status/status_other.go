//go:build !linux

package status

func read(string) (Snapshot, error) { return Snapshot{}, ErrUnsupported }
