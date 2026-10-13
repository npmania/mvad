//go:build !linux && !darwin

package lock

import "errors"

const defaultPath = "/run/mvad/lock"

func acquireRoot(bool) (func(), error) {
	return nil, errors.New("lock: unsupported platform")
}
