package connector

import (
	"encoding/xml"
	"strings"
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

func TestIncludeMAMEndBound(t *testing.T) {
	if includeMAMEndBound("", "") {
		t.Fatal("expected account-wide MAM query to omit end bound")
	}
	if !includeMAMEndBound("", "contact@example.com") {
		t.Fatal("expected per-contact MAM query to include end bound")
	}
	if !includeMAMEndBound("room@example.com", "") {
		t.Fatal("expected MUC MAM query to include end bound")
	}
}

func TestIsValidMAMForwardedJID(t *testing.T) {
	if !isValidMAMForwardedJID("user@example.com/resource") {
		t.Fatal("expected full JID to be valid")
	}
	if !isValidMAMForwardedJID("") {
		t.Fatal("expected empty from JID to be accepted")
	}
	if isValidMAMForwardedJID("bad jid@example.com") {
		t.Fatal("expected JID with spaces to be invalid")
	}
}

func TestMarshalMAMQueryIncludesNamespace(t *testing.T) {
	for _, ns := range []string{NSMAM2, NSMAM1, NSMAM0} {
		query := &MAMQuery{
			XMLName: xml.Name{Space: ns, Local: "query"},
			QueryID: "test-query",
			Form: XData{
				Type: "submit",
				Fields: []XDataField{
					{Var: "FORM_TYPE", Type: "hidden", Values: []string{ns}},
				},
			},
		}

		out, err := xml.Marshal(query)
		if err != nil {
			t.Fatalf("failed to marshal MAM query for %s: %v", ns, err)
		}
		serialized := string(out)
		if !strings.Contains(serialized, `xmlns="`+ns+`"`) {
			t.Fatalf("MAM query for %s lost its namespace during marshaling: %s", ns, serialized)
		}
		if !strings.Contains(serialized, `queryid="test-query"`) {
			t.Fatalf("MAM query for %s lost its query id during marshaling: %s", ns, serialized)
		}
	}
}

func TestMarshalMAMFinIncludesNamespace(t *testing.T) {
	fin := &MAMFin{XMLName: xml.Name{Space: NSMAM2, Local: "fin"}, Complete: true}

	out, err := xml.Marshal(fin)
	if err != nil {
		t.Fatalf("failed to marshal MAM fin: %v", err)
	}
	serialized := string(out)
	if !strings.Contains(serialized, `xmlns="`+NSMAM2+`"`) {
		t.Fatalf("MAM fin lost its namespace during marshaling: %s", serialized)
	}
	if !strings.Contains(serialized, `complete="true"`) {
		t.Fatalf("MAM fin lost its complete flag during marshaling: %s", serialized)
	}
}

func assertErr(msg string) error {
	return stanzaErrorString(msg)
}

type stanzaErrorString string

func (e stanzaErrorString) Error() string {
	return string(e)
}
