//go:build linux || darwin

package lockdown

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func writeMarker() {
	dir := filepath.Dir(markerPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("lockdown: mkdir %s: %v", dir, err)
		return
	}
	// lock creates the dir 0700, hiding the marker from the unprivileged
	// GUI; the lock, pidfiles, and tokens keep their own 0600.
	if err := os.Chmod(dir, 0755); err != nil {
		log.Printf("lockdown: chmod %s: %v", dir, err)
	}
	if err := os.WriteFile(markerPath, nil, 0644); err != nil {
		log.Printf("lockdown: write %s: %v", markerPath, err)
	}
}

func removeMarker() {
	if err := os.Remove(markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("lockdown: remove %s: %v", markerPath, err)
	}
}

func writeScript(s string) error {
	dir := filepath.Dir(scriptPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("lockdown: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".lockdown-*"+filepath.Ext(scriptPath))
	if err != nil {
		return fmt.Errorf("lockdown: tempfile: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(s); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("lockdown: write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0644); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, scriptPath); err != nil {
		os.Remove(name)
		return fmt.Errorf("lockdown: rename %s: %w", scriptPath, err)
	}
	return nil
}
