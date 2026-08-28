package connector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
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

	if got := contactLabelFromJID("+15551234567@cheogram.com"); got != "+15551234567" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "+15551234567")
	}
	if got := contactLabelFromJID("+15550102000@cheogram.com"); got != "+15550102000" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "+15550102000")
	}
	if got := contactLabelFromJID("+447123456789@cheogram.com"); got != "+447123456789" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "+447123456789")
	}
	if got := contactLabelFromJID("room@example.com/Nick"); got != "Nick" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "Nick")
	}
	if got := contactLabelFromJID("alice@example.com"); got != "alice" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "alice")
	}
}

func TestGroupInitialsFromLabels(t *testing.T) {
	t.Parallel()

	if got := groupInitialsFromLabels([]string{"Joe Apple", "Bob Pear", "Cindy Banana"}); got != "JBC" {
		t.Fatalf("groupInitialsFromLabels() = %q, want %q", got, "JBC")
	}
	if got := groupInitialsFromLabels([]string{"Joe Apple", "+15550102000", "Cindy Banana"}); got != "J1C" {
		t.Fatalf("groupInitialsFromLabels() = %q, want %q", got, "J1C")
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

func TestCheogramGroupFallbackWithoutMetadata(t *testing.T) {
	t.Parallel()

	roomJID := "+15551230001,+15551230002@cheogram.com"
	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey:    networkid.PortalKey{ID: MakePortalID(roomJID)},
			Name:         roomJID,
			RoomType:     database.RoomTypeDM,
			NameIsCustom: false,
		},
	}

	if !portalUsesGroupChat(portal) {
		t.Fatal("expected cheogram comma-separated room to be treated as a group chat")
	}

	contacts := portalContacts(portal)
	wantContacts := []string{"+15551230001@cheogram.com", "+15551230002@cheogram.com"}
	if len(contacts) != len(wantContacts) {
		t.Fatalf("portalContacts() = %v, want %v", contacts, wantContacts)
	}
	for i, want := range wantContacts {
		if contacts[i] != want {
			t.Fatalf("portalContacts()[%d] = %q, want %q", i, contacts[i], want)
		}
	}

	if !portalLooksAutoNamed(portal, "jmp") {
		t.Fatal("expected cheogram group room without metadata to be treated as auto-named")
	}

	if !portalMatchesContact(portal, "+15551230002@cheogram.com") {
		t.Fatal("expected cheogram group room to match one of its contacts")
	}
	if portalMatchesContact(portal, "+15550102400@cheogram.com") {
		t.Fatal("did not expect cheogram group room to match an unrelated contact")
	}

	if got := groupRoomNameFromJID(context.Background(), nil, roomJID, "", "jmp"); got != "+15551230001,+15551230002 (jmp)" {
		t.Fatalf("groupRoomNameFromJID() = %q, want %q", got, "+15551230001,+15551230002 (jmp)")
	}
}

func TestSimpleConfigUnmarshalYAMLInlineMapping(t *testing.T) {
	var cfg struct {
		Network SimpleConfig `yaml:"network"`
	}
	if err := yaml.Unmarshal([]byte("network:\n  contact_name_suffix: chat\n"), &cfg); err != nil {
		t.Fatalf("failed to unmarshal inline config: %v", err)
	}
	if cfg.Network.ContactNameSuffix != "chat" {
		t.Fatalf("unexpected contact name suffix: %q", cfg.Network.ContactNameSuffix)
	}
	if err := yaml.Unmarshal([]byte("network: custom\n"), &cfg); err != nil {
		t.Fatalf("failed to unmarshal scalar config: %v", err)
	}
	if cfg.Network.ContactNameSuffix != "custom" {
		t.Fatalf("unexpected shorthand contact name suffix: %q", cfg.Network.ContactNameSuffix)
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "simple-config.yaml")
	if err := os.WriteFile(configPath, []byte("contact_name_suffix: bridge\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir to temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	if err := yaml.Unmarshal([]byte("network: simple-config.yaml\n"), &cfg); err != nil {
		t.Fatalf("failed to unmarshal file-backed config: %v", err)
	}
	if cfg.Network.ContactNameSuffix != "bridge" {
		t.Fatalf("unexpected contact name suffix: %q", cfg.Network.ContactNameSuffix)
	}
}
