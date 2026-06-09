package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestSuffixFormatting(t *testing.T) {
	t.Parallel()

	if got := appendContactNameSuffix("Joe Apple", "jmp"); got != "Joe Apple (jmp)" {
		t.Fatalf("appendContactNameSuffix() = %q, want %q", got, "Joe Apple (jmp)")
	}
	if got := appendContactNameSuffix("Joe Apple (jmp)", "jmp"); got != "Joe Apple (jmp)" {
		t.Fatalf("appendContactNameSuffix() = %q, want %q", got, "Joe Apple (jmp)")
	}
	if got := appendContactNameSuffix("Joe Apple", ""); got != "Joe Apple" {
		t.Fatalf("appendContactNameSuffix() = %q, want %q", got, "Joe Apple")
	}
	if got := stripContactNameSuffix("Joe Apple (jmp)", "jmp"); got != "Joe Apple" {
		t.Fatalf("stripContactNameSuffix() = %q, want %q", got, "Joe Apple")
	}
}

func TestContactLabelFromJID(t *testing.T) {
	t.Parallel()

	if got := contactLabelFromJID("+15551234567@cheogram.com"); got != "5551234567" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "5551234567")
	}
	if got := contactLabelFromJID("+15550102000@cheogram.com"); got != "4569876543" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "4569876543")
	}
	if got := contactLabelFromJID("room@example.com/Nick"); got != "Nick" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "Nick")
	}
}

func TestGroupInitialsFromLabels(t *testing.T) {
	t.Parallel()

	if got := groupInitialsFromLabels([]string{"Joe Apple", "Bob Pear", "Cindy Banana"}); got != "JBC" {
		t.Fatalf("groupInitialsFromLabels() = %q, want %q", got, "JBC")
	}
	if got := groupInitialsFromLabels([]string{"Joe Apple", "4569876543", "Cindy Banana"}); got != "J4C" {
		t.Fatalf("groupInitialsFromLabels() = %q, want %q", got, "J4C")
	}
}

func TestPortalLooksAutoNamed(t *testing.T) {
	t.Parallel()

	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey:    networkid.PortalKey{ID: MakePortalID("+15551230001,+15551230002,+15551230003@cheogram.com")},
			Name:         "JBP (jmp)",
			RoomType:     database.RoomTypeDefault,
			Metadata:     &PortalMetadata{IsMUC: true},
			NameIsCustom: false,
		},
	}
	if !portalLooksAutoNamed(portal, "jmp") {
		t.Fatal("expected group portal to be treated as auto-named")
	}

	portal.Name = "Joe Apple (jmp)"
	if portalLooksAutoNamed(portal, "jmp") {
		t.Fatal("expected manual group name to be treated as explicit")
	}

	portal = &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey: networkid.PortalKey{ID: MakePortalID("+15551230001@cheogram.com")},
			RoomType:  database.RoomTypeDM,
		},
	}
	if !portalLooksAutoNamed(portal, "jmp") {
		t.Fatal("expected DM portal to be treated as auto-named")
	}
}
