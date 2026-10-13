//go:build unix

package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOpenDirOwner(t *testing.T) {
	dir := t.TempDir()
	me := &CallingUser{UID: os.Getuid(), GID: os.Getgid()}
	other := &CallingUser{UID: os.Getuid() + 1, GID: os.Getgid()}

	r, err := openDir(dir, me, false)
	if err != nil {
		t.Fatalf("openDir as owner: %v", err)
	}
	r.Close()
	if r, err := openDir(dir, other, false); err == nil {
		r.Close()
		t.Error("openDir for another user succeeded")
	}

	sub := filepath.Join(dir, "a", "b")
	r, err = openDir(sub, me, true)
	if err != nil {
		t.Fatalf("openDir create: %v", err)
	}
	r.Close()
	for _, d := range []string{filepath.Join(dir, "a"), sub} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.IsDir() || fi.Mode().Perm() != 0700 {
			t.Errorf("%s: mode %v, want dir 0700", d, fi.Mode())
		}
	}

	foreign := filepath.Join(dir, "x")
	if r, err := openDir(foreign, other, true); err == nil {
		r.Close()
		t.Error("openDir created a directory inside another user's")
	}
	if _, err := os.Stat(foreign); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s exists after a refused create", foreign)
	}
}

func TestSaveFollowsDirSymlink(t *testing.T) {
	home := setHome(t)
	dst := t.TempDir()
	if err := os.Symlink(dst, filepath.Join(home, "mvad")); err != nil {
		t.Fatal(err)
	}
	if err := (&Config{AccountToken: "x"}).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "config.json")); err != nil {
		t.Errorf("config not written through the symlink: %v", err)
	}
	c, err := Load()
	if err != nil || c.AccountToken != "x" {
		t.Errorf("Load = %+v, %v", c, err)
	}
}

func TestSaveReplacesFileSymlink(t *testing.T) {
	home := setHome(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "mvad")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("Load followed config.json out of its directory")
	}
	if err := (&Config{AccountToken: "x"}).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("symlink target rewritten: %q", b)
	}
	fi, err := os.Lstat(filepath.Join(dir, "config.json"))
	if err != nil || !fi.Mode().IsRegular() {
		t.Errorf("config.json after Save: %v, %v", fi, err)
	}
}

func TestLoadRejectsFIFO(t *testing.T) {
	home := setHome(t)
	dir := filepath.Join(home, "mvad")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "config.json"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Load()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Load accepted a FIFO")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Load blocked on a FIFO")
	}
}
