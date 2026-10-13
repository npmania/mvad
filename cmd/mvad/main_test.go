package main

import "testing"

func TestRelayFilter(t *testing.T) {
	tests := []struct {
		list []string
		host string
		name string
		city bool
		want bool
	}{
		{nil, "se-got-wg-001", "Sweden", false, true},
		{[]string{"se"}, "se-got-wg-001", "Sweden", false, true},
		{[]string{"SE"}, "se-got-wg-001", "Sweden", false, true},
		{[]string{"sweden"}, "se-got-wg-001", "Sweden", false, true},
		{[]string{"jp", "se"}, "se-got-wg-001", "Sweden", false, true},
		{[]string{"jp"}, "se-got-wg-001", "Sweden", false, false},
		{[]string{"got"}, "se-got-wg-001", "Gothenburg", true, true},
		{[]string{"Gothenburg"}, "se-got-wg-001", "Gothenburg", true, true},
		{[]string{"se"}, "se-got-wg-001", "Gothenburg", true, false},
		{[]string{"sto"}, "se-sto-br-001", "Stockholm", true, true},
	}
	for _, tt := range tests {
		cc, ct := locCodes(tt.host)
		code := cc
		if tt.city {
			code = ct
		}
		if got := matchesAny(tt.list, tt.name, code); got != tt.want {
			t.Errorf("matchesAny(%q, %q, %q) = %v, want %v", tt.list, tt.name, code, got, tt.want)
		}
	}
}
