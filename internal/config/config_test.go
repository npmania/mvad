package config

import (
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func setHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	return dir
}

func TestLoadMissing(t *testing.T) {
	setHome(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(*c, Config{}) {
		t.Errorf("got %+v, want zero Config", c)
	}
}

func TestRoundTrip(t *testing.T) {
	setHome(t)
	want := &Config{
		AccountToken:    "1234567890123456",
		DeviceID:        "device-uuid",
		PrivateKey:      "iLDdRBrIGRPVx5xq1tHRvF3i+nF2qXSfBpoR6vWzYxw=",
		LastRelay:       "se-sto-wg-001",
		RelayCache:      json.RawMessage(`[{"hostname":"se-sto-wg-001","ipv4":"1.2.3.4"}]`),
		RelaysFetchedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := want.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AccountToken != want.AccountToken ||
		got.DeviceID != want.DeviceID ||
		got.PrivateKey != want.PrivateKey ||
		got.LastRelay != want.LastRelay ||
		!got.RelaysFetchedAt.Equal(want.RelaysFetchedAt) {
		t.Errorf("scalar mismatch:\n got=%+v\nwant=%+v", got, want)
	}
	var gotR, wantR any
	if err := json.Unmarshal(got.RelayCache, &gotR); err != nil {
		t.Fatalf("unmarshal got cache: %v", err)
	}
	if err := json.Unmarshal(want.RelayCache, &wantR); err != nil {
		t.Fatalf("unmarshal want cache: %v", err)
	}
	if !reflect.DeepEqual(gotR, wantR) {
		t.Errorf("relay cache mismatch:\n got=%v\nwant=%v", gotR, wantR)
	}
}

func TestSaveMode(t *testing.T) {
	setHome(t)
	c := &Config{AccountToken: "x"}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	p, err := path()
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0600 {
		t.Errorf("file mode = %o, want 0600", mode)
	}
	dst, err := os.Stat(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if mode := dst.Mode().Perm(); mode&0077 != 0 {
		t.Errorf("dir mode = %o, leaks bits to group/other", mode)
	}
}

func TestSaveOverwrites(t *testing.T) {
	setHome(t)
	if err := (&Config{AccountToken: "first"}).Save(); err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	if err := (&Config{AccountToken: "second"}).Save(); err != nil {
		t.Fatalf("Save 2: %v", err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AccountToken != "second" {
		t.Errorf("got %q, want %q", c.AccountToken, "second")
	}
}

func TestPathPrefersXDG(t *testing.T) {
	xdg := t.TempDir()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", home)
	p, err := path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdg, "mvad", "config.json"); p != want {
		t.Errorf("Path = %q, want %q", p, want)
	}
}

func TestPathFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	p, err := path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "mvad", "config.json"); p != want {
		t.Errorf("Path = %q, want %q", p, want)
	}
}

func TestPathSudoUserRedirectsToInvokerHome(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	name := os.Getenv("SUDO_USER")
	if name == "" {
		t.Skip("SUDO_USER not set")
	}
	u, err := user.Lookup(name)
	if err != nil {
		t.Fatalf("user.Lookup(%q): %v", name, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/root")
	p, err := path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(u.HomeDir, ".config", "mvad", "config.json"); p != want {
		t.Errorf("Path = %q, want %q", p, want)
	}
}

func TestPathIgnoresSudoUserWhenNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires non-root")
	}
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("SUDO_USER", "nonexistent-user")
	p, err := path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "mvad", "config.json"); p != want {
		t.Errorf("Path = %q, want %q", p, want)
	}
}
