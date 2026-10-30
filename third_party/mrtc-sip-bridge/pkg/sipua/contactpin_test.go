package sipua

import (
	"testing"

	"github.com/emiago/diago"
	"github.com/emiago/sipgo/sip"
)

func newPinTestTxn(pin bool) *diagoTxn {
	req := sip.NewRequest(sip.REGISTER, sip.Uri{Host: "registrar.example.com", Port: 5008})
	contact := sip.ContactHeader{Address: sip.Uri{Host: "10.0.0.1", Port: 5060}}
	req.AppendHeader(&contact)
	return &diagoTxn{
		rt:          &diago.RegisterTransaction{Origin: req},
		pinContact:  pin,
		contactHost: "203.0.113.7",
		contactPort: 5060,
	}
}

func TestDiagoTxnPinContactHeader(t *testing.T) {
	tx := newPinTestTxn(true)
	// Simulate diago's rport rewrite: the registrar echoed an ephemeral NAT port.
	c := tx.rt.Origin.Contact()
	if c == nil {
		t.Fatal("no contact header")
	}
	c.Address.Port = 59914
	tx.rt.Origin.ReplaceHeader(c)

	tx.pinContactHeader()
	if got := tx.Contact(); got != "203.0.113.7:5060" {
		t.Fatalf("pinned contact = %q, want 203.0.113.7:5060", got)
	}

	// Without PinContact the rewritten contact is left alone.
	plain := newPinTestTxn(false)
	pc := plain.rt.Origin.Contact()
	pc.Address.Port = 59914
	plain.rt.Origin.ReplaceHeader(pc)
	plain.pinContactHeader()
	if got := plain.Contact(); got != "10.0.0.1:59914" {
		t.Fatalf("unpinned contact = %q, want 10.0.0.1:59914", got)
	}
}
