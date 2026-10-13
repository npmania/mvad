//go:build darwin

package lock

// macOS has no /run; /var/run is likewise cleared at boot.
const defaultPath = "/var/run/mvad/lock"
