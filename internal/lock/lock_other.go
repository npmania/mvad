//go:build !linux

package lock

import "errors"

func acquireRoot(bool) (func(), error) {
	return nil, errors.New("lock: unsupported platform")
}
