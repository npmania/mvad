//go:build darwin

package wg

import "testing"

func TestDownAbsent(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"utun32767", true},
		{"", false},
	}
	for _, c := range cases {
		err := Down(c.name)
		if (err == nil) != c.ok {
			t.Errorf("Down(%q) = %v", c.name, err)
		}
	}
}
