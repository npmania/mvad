//go:build linux

package split

import (
	"strings"
	"testing"
)

func TestBuildScript(t *testing.T) {
	got := buildScript()
	wants := []string{
		"add table inet mvad-split",
		"delete table inet mvad-split",
		"table inet mvad-split {",
		"type route hook output priority -150;",
		`socket cgroupv2 level 1 "mvad-split" meta mark set 0xca6c`,
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("script missing %q\n--- got ---\n%s", w, got)
		}
	}
}
