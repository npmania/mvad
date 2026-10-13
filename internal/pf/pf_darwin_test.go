//go:build darwin

package pf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseToken(t *testing.T) {
	tests := []struct {
		out string
		tok string
		ok  bool
	}{
		{"No ALTQ support in kernel\nALTQ related functions disabled\npf enabled\nToken : 13472326879873332454\n", "13472326879873332454", true},
		{"pf enabled\nToken : 7", "7", true},
		{"pf enabled\n", "", false},
		{"Token : x\n", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		tok, ok := parseToken([]byte(tt.out))
		if tok != tt.tok || ok != tt.ok {
			t.Errorf("parseToken(%q) = %q, %v, want %q, %v", tt.out, tok, ok, tt.tok, tt.ok)
		}
	}
}

func TestHasHook(t *testing.T) {
	tests := []struct {
		rules string
		want  bool
	}{
		{"scrub-anchor \"com.apple/*\" all fragment reassemble\nanchor \"com.apple/*\" all\n", true},
		{"scrub-anchor \"com.apple/*\" all fragment reassemble\n", false},
		{"anchor \"other/*\" all\n", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := hasHook([]byte(tt.rules)); got != tt.want {
			t.Errorf("hasHook(%q) = %v, want %v", tt.rules, got, tt.want)
		}
	}
}

// The root tests only ever pass traffic: pf runs with no mvad rules
// loaded, and the throwaway anchor holds a single pass rule.
func needRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	for _, a := range []string{"com.apple/mvad", "com.apple/mvad-lockdown"} {
		if HasRules(a) {
			t.Skipf("%s holds rules", a)
		}
	}
}

func TestLoadFlush(t *testing.T) {
	needRoot(t)
	if err := CheckHook(); err != nil {
		t.Fatal(err)
	}
	const anchor = "com.apple/mvadtest"
	t.Cleanup(func() { Flush(anchor) })
	if err := Load(anchor, "pass quick on lo0 no state\n"); err != nil {
		t.Fatal(err)
	}
	if !HasRules(anchor) {
		t.Error("HasRules = false after Load")
	}
	if err := Load(anchor, "bogus\n"); err == nil {
		t.Error("Load of bad rules succeeded")
	} else {
		t.Logf("bad Load: %v", err)
	}
	if !HasRules(anchor) {
		t.Error("failed Load dropped the old rules")
	}
	for range 2 {
		if err := Flush(anchor); err != nil {
			t.Fatal(err)
		}
		if HasRules(anchor) {
			t.Error("HasRules = true after Flush")
		}
	}
}

func TestEnableRelease(t *testing.T) {
	needRoot(t)
	was := Enabled()
	file := filepath.Join(t.TempDir(), "pf.token")
	t.Cleanup(func() { Release(file) })
	var toks []string
	for range 2 {
		if err := Enable(file); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		toks = append(toks, string(b))
	}
	if toks[0] == toks[1] {
		t.Errorf("Enable reused token %q", toks[0])
	}
	if !Enabled() {
		t.Error("Enabled = false after Enable")
	}
	for range 2 {
		if err := Release(file); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("token file left after Release: %v", err)
	}
	if got := Enabled(); got != was {
		t.Errorf("Enabled = %v after Release, want %v", got, was)
	}
	if err := release("1"); err != nil {
		t.Errorf("release of unknown token: %v", err)
	}
}
