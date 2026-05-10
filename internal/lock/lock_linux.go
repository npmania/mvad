//go:build linux

package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// Reentrant within a process so that verbs which call into other
// verbs (e.g. reconnect → disconnect+connect) don't deadlock against
// their own flock.
var (
	mu    sync.Mutex
	depth int
	file  *os.File
)

func acquireRoot() (func(), error) {
	mu.Lock()
	defer mu.Unlock()
	if depth > 0 {
		depth++
		return releaseOnce(func() {
			mu.Lock()
			defer mu.Unlock()
			depth--
		}), nil
	}
	dir := filepath.Dir(Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("lock: mkdir %s: %w", dir, err)
	}
	f, err := os.OpenFile(Path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("lock: open %s: %w", Path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock: flock %s: %w", Path, err)
	}
	file = f
	depth = 1
	return releaseOnce(func() {
		mu.Lock()
		defer mu.Unlock()
		depth--
		if depth > 0 {
			return
		}
		unix.Flock(int(file.Fd()), unix.LOCK_UN)
		file.Close()
		file = nil
	}), nil
}

func releaseOnce(fn func()) func() {
	var once sync.Once
	return func() { once.Do(fn) }
}
