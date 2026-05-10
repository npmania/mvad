//go:build !linux

package lock

import "errors"

func acquireRoot() (func(), error) {
	return nil, errors.New("lock: unsupported platform")
}
