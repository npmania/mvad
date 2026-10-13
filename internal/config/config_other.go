//go:build !unix

package config

import "io/fs"

// No uids to compare off unix; refuse.
func ownedBy(fs.FileInfo, int) bool { return false }
