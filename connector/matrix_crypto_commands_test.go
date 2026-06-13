package connector

import "testing"

func TestMatrixEncryptionResetRequested(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "empty", args: nil, want: false},
		{name: "no confirmation", args: []string{"reset"}, want: false},
		{name: "yes", args: []string{"yes"}, want: true},
		{name: "dash yes", args: []string{"--yes"}, want: true},
		{name: "force", args: []string{"force"}, want: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := matrixEncryptionResetRequested(tt.args); got != tt.want {
				t.Fatalf("matrixEncryptionResetRequested(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
