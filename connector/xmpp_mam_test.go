package connector

import "testing"

func TestBestMAMNamespace(t *testing.T) {
	tests := []struct {
		name     string
		features map[string]bool
		want     string
	}{
		{name: "none", features: nil, want: ""},
		{name: "mam0", features: map[string]bool{NSMAM0: true}, want: NSMAM0},
		{name: "mam1", features: map[string]bool{NSMAM1: true}, want: NSMAM1},
		{name: "mam2 preferred", features: map[string]bool{NSMAM0: true, NSMAM2: true}, want: NSMAM2},
	}

	for _, tt := range tests {
		if got := bestMAMNamespace(tt.features); got != tt.want {
			t.Fatalf("%s: bestMAMNamespace() = %q, want %q", tt.name, got, tt.want)
		}
	}
}
