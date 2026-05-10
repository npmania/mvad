//go:build linux

package lockdown

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func on(relayIPs []netip.Addr) error {
	script := buildScript(relayIPs)
	if err := writeScript(script); err != nil {
		return err
	}
	if err := runNft(script); err != nil {
		return err
	}
	writeMarker()
	return nil
}

func off() error {
	delErr := delTable()
	rmErr := os.Remove(scriptPath)
	if errors.Is(rmErr, os.ErrNotExist) {
		rmErr = nil
	}
	if delErr == nil {
		removeMarker()
	}
	return errors.Join(delErr, rmErr)
}

func refresh(relayIPs []netip.Addr) error {
	if _, err := os.Stat(scriptPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	script := buildScript(relayIPs)
	if err := writeScript(script); err != nil {
		return err
	}
	if !tablePresent() {
		return nil
	}
	if err := runNft(script); err != nil {
		return err
	}
	writeMarker()
	return nil
}

func active() bool {
	_, err := os.Stat(markerPath)
	return err == nil
}

func writeMarker() {
	dir := filepath.Dir(markerPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("lockdown: mkdir %s: %v", dir, err)
		return
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
	tmp, err := os.CreateTemp(dir, ".lockdown-*.nft")
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

func runNft(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("lockdown: nft -f: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func delTable() error {
	cmd := exec.Command("nft", "delete", "table", "inet", tableName)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	s := string(out)
	if strings.Contains(s, "No such file or directory") || strings.Contains(s, "does not exist") {
		return nil
	}
	return fmt.Errorf("lockdown: nft delete table inet %s: %w: %s", tableName, err, bytes.TrimSpace(out))
}

func tablePresent() bool {
	cmd := exec.Command("nft", "list", "table", "inet", tableName)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.Run() == nil
}
