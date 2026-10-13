//go:build unix

package config

import (
	"io/fs"
	"syscall"
)

func ownedBy(fi fs.FileInfo, uid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}
