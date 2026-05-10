// Package lock serializes mvad's state-mutating verbs via a flock
// on /run/mvad/lock.
package lock

import "errors"

// Path is the lock file used by AcquireRoot. It is a var so tests
// can redirect to a writable location.
var Path = "/run/mvad/lock"

// ErrLocked is returned when the lock is held by another process.
var ErrLocked = errors.New("another mvad invocation is running; aborting")

// AcquireRoot takes a non-blocking exclusive flock on Path and
// returns a release func that drops the lock and closes the fd.
// Callers must run as root since /run/mvad is root-owned.
func AcquireRoot() (release func(), err error) { return acquireRoot() }
