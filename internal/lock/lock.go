// Package lock serializes mvad's state-mutating verbs via a flock
// on Path.
package lock

import "errors"

// Path is the lock file used by AcquireRoot: /run/mvad/lock on Linux,
// /var/run/mvad/lock on macOS. It is a var so tests can redirect to a
// writable location.
var Path = defaultPath

// ErrLocked is returned when the lock is held by another process.
var ErrLocked = errors.New("another mvad invocation is running; aborting")

// AcquireRoot takes a non-blocking exclusive flock on Path and
// returns a release func that drops the lock and closes the fd.
// Callers must run as root since Path's directory is root-owned.
func AcquireRoot() (release func(), err error) { return acquireRoot(false) }

// AcquireRootBlock is AcquireRoot but waits for the lock instead of
// returning ErrLocked. For long-lived callers that must ride out a
// brief competing invocation.
func AcquireRootBlock() (release func(), err error) { return acquireRoot(true) }
