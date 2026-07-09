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

// Reentrant within a process: nested calls share the flock; the
// last release drops it, regardless of the order callers release in.
var (
	mu    sync.Mutex
	depth int
	file  *os.File
)

func acquireRoot(block bool) (func(), error) {
	mu.Lock()
	defer mu.Unlock()
	if depth == 0 {
		dir := filepath.Dir(Path)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("lock: mkdir %s: %w", dir, err)
		}
		f, err := os.OpenFile(Path, os.O_RDWR|os.O_CREATE, 0600)
		if err != nil {
			return nil, fmt.Errorf("lock: open %s: %w", Path, err)
		}
		how := unix.LOCK_EX | unix.LOCK_NB
		if block {
			how = unix.LOCK_EX
		}
		if err := unix.Flock(int(f.Fd()), how); err != nil {
			f.Close()
			if errors.Is(err, unix.EWOULDBLOCK) {
				return nil, ErrLocked
			}
			return nil, fmt.Errorf("lock: flock %s: %w", Path, err)
		}
		file = f
	}
	depth++
	var once sync.Once
	return func() { once.Do(release) }, nil
}

func release() {
	mu.Lock()
	defer mu.Unlock()
	depth--
	if depth > 0 {
		return
	}
	unix.Flock(int(file.Fd()), unix.LOCK_UN)
	file.Close()
	file = nil
}
