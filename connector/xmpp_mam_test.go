package connector

import (
	"testing"

	"gosrc.io/xmpp/stanza"
)

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

func TestFormatIQError(t *testing.T) {
	err := &stanza.Err{Type: stanza.ErrorTypeCancel, Reason: "service-unavailable", Text: "archive disabled"}
	got := formatIQError(err)
	want := "cancel/service-unavailable/archive disabled"
	if got != want {
		t.Fatalf("formatIQError() = %q, want %q", got, want)
	}
}

func TestIsServiceUnavailableMAMError(t *testing.T) {
	if !isServiceUnavailableMAMError(assertErr("MAM request failed: error (cancel/service-unavailable)")) {
		t.Fatal("expected service-unavailable error to match")
	}
	if isServiceUnavailableMAMError(assertErr("MAM request failed: error (modify/bad-request)")) {
		t.Fatal("did not expect bad-request to match")
	}
}

func assertErr(msg string) error {
	return stanzaErrorString(msg)
}

type stanzaErrorString string

func (e stanzaErrorString) Error() string {
	return string(e)
}
