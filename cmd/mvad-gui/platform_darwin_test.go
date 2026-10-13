//go:build darwin

package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestOsaResult(t *testing.T) {
	exit1 := errors.New("exit status 1")
	tests := []struct {
		out     string
		err     error
		wantOut string
		wantErr string
	}{
		{"connected\n\n", nil, "connected\n", ""},
		{"\n", nil, "", ""},
		{"99:208: execution error: progress\nmvad: relay not found\n (3)\n", exit1, "progress\nmvad: relay not found\n", "exit status 3"},
		{"0:17: execution error: User canceled. (-128)\n", exit1, "", "authentication canceled"},
		{"0:17: execution error: Abgebrochen. (-128)\n", exit1, "", "authentication canceled"},
		{"osascript: garbage\n", exit1, "osascript: garbage\n", exit1.Error()},
	}
	for _, tt := range tests {
		out, err := osaResult([]byte(tt.out), tt.err)
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		if out != tt.wantOut || errStr != tt.wantErr {
			t.Errorf("osaResult(%q) = %q, %q; want %q, %q", tt.out, out, errStr, tt.wantOut, tt.wantErr)
		}
	}
}

// TestAsRootQuoting runs asRoot without the privilege clause and checks
// that every argument reaches the shell intact.
func TestAsRootQuoting(t *testing.T) {
	script := strings.Replace(asRoot, " with administrator privileges", "", 1)
	if script == asRoot {
		t.Fatal("asRoot has no privilege clause")
	}
	args := []string{"it's", `a "q"`, "$HOME", `back\slash`, "nl\nx", "ü", "--allow-lan", "--", "-e", "; true"}
	argv := append([]string{"-e", script, "--", "/usr/bin/printf", `[%s]\n`}, args...)
	out, err := osaResult(exec.Command("/usr/bin/osascript", argv...).CombinedOutput())
	if err != nil {
		t.Fatalf("osascript: %v: %s", err, out)
	}
	var want strings.Builder
	for _, a := range args {
		want.WriteString("[" + a + "]\n")
	}
	if out != want.String() {
		t.Errorf("got %q, want %q", out, want.String())
	}

	argv = []string{"-e", script, "--", "/bin/sh", "-c", "echo mvad: no; exit 3"}
	out, err = osaResult(exec.Command("/usr/bin/osascript", argv...).CombinedOutput())
	if out != "mvad: no\n" || err == nil || err.Error() != "exit status 3" {
		t.Errorf("failure = %q, %v; want %q, exit status 3", out, err, "mvad: no\n")
	}
}
