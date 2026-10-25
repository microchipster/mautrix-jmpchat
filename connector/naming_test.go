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
	if got := contactLabelFromJID("69061;phone-context=ca-us.phone-context.soprani.ca@cheogram.com"); got != "69061" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "69061")
	}
	if got := contactLabelFromJID("69061@cheogram.com"); got != "69061" {
		t.Fatalf("contactLabelFromJID() = %q, want %q", got, "69061")
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

	portal.Name = "SM (jmp)"
	if !portalLooksAutoNamed(portal, "jmp") {
		t.Fatal("expected short initials to be treated as auto-named so they self-heal")
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

func TestDMRoomNameWithShortCodePhoneContext(t *testing.T) {
	t.Parallel()

	got := dmRoomName(context.Background(), nil, "69061;phone-context=ca-us.phone-context.soprani.ca@cheogram.com", "", "jmp")
	if got != "69061 (jmp)" {
		t.Fatalf("dmRoomName() = %q, want %q", got, "69061 (jmp)")
	}
}

func TestAutoRenameDetectionWithPhoneContextAndCustomFlag(t *testing.T) {
	t.Parallel()

	// Even if NameIsCustom is true (due to bridgev2 setting it on room creation),
	// if the name has phone-context or is auto-named short code/phone, it should be recognized
	// and desired name should compute the clean name.
	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey:    networkid.PortalKey{ID: MakePortalID("69061;phone-context=ca-us.phone-context.soprani.ca@cheogram.com")},
			Name:         "69061;phone-context=ca-us.phone-context.soprani.ca (jmp)",
			RoomType:     database.RoomTypeDM,
			NameIsCustom: true,
		},
	}

	if !portalLooksAutoNamed(portal, "jmp") {
		t.Fatal("expected portal with phone-context to be recognized as auto-named")
	}

	desired := portalDesiredName(context.Background(), nil, portal, "jmp")
	if desired != "69061 (jmp)" {
		t.Fatalf("portalDesiredName() = %q, want %q", desired, "69061 (jmp)")
	}
}

func TestInitialsLikeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contacts int
		want     bool
	}{
		{"SM", 3, true},
		{"SMR", 3, true},
		{"SM", 2, true},
		{"S", 3, true},
		{"1", 3, true},
		{"SMR", 2, false},
		{"SMRX", 3, false},
		{"Sm", 3, false},
		{"S M", 3, false},
		{"", 3, false},
		{"SM", 0, false},
	}
	for _, tc := range tests {
		if got := initialsLikeName(tc.name, tc.contacts); got != tc.want {
			t.Errorf("initialsLikeName(%q, %d) = %v, want %v", tc.name, tc.contacts, got, tc.want)
		}
	}
}

func TestNormalizeContactName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"apostrophe", "O'Brien", "O'Brien"},
		{"typographic apostrophe", "O’Brien", "O’Brien"},
		{"double quotes", `"Bob"`, `"Bob"`},
		{"multiple spaces", "Mary   Jane", "Mary Jane"},
		{"tabs and newlines", "\t Mary \n Jane \r\n", "Mary Jane"},
		{"control chars dropped", "Bob\x00 \x07Builder", "Bob Builder"},
		{"emoji preserved", "José 😀", "José 😀"},
		{"emoji zwj preserved", "👨\u200d👩\u200d👧", "👨\u200d👩\u200d👧"},
		{"mixed", "  O'Brien \t & \n Sons  ", "O'Brien & Sons"},
		{"empty", "   \t\n ", ""},
		{"only control", "\x00\x01\x02", ""},
	}
	for _, tc := range tests {
		if got := normalizeContactName(tc.in); got != tc.want {
			t.Errorf("normalizeContactName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestContactInitialSpecialChars(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"O'Brien":    "O",
		"d'Artagnan": "D",
		"-Smith":     "S",
		"🌟Star":      "S",
		"123 Main":   "1",
		"!!!":        "",
		"":           "",
		"(O'Brien)":  "O",
	}
	for input, want := range tests {
		if got := contactInitial(input); got != want {
			t.Errorf("contactInitial(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGroupInitialsSpecialChars(t *testing.T) {
	t.Parallel()

	if got := groupInitialsFromLabels([]string{"O'Brien", "Smith", "Roe"}); got != "OSR" {
		t.Fatalf("groupInitialsFromLabels() = %q, want %q", got, "OSR")
	}
	if got := groupInitialsFromLabels([]string{"O'Brien", "d'Artagnan", "+15550102000"}); got != "OD1" {
		t.Fatalf("groupInitialsFromLabels() = %q, want %q", got, "OD1")
	}
	if got := groupInitialsFromLabels([]string{"O'Brien", "", "Roe"}); got != "OR" {
		t.Fatalf("groupInitialsFromLabels() = %q, want %q", got, "OR")
	}
}

func TestGroupRoomNameExplicitSpecialChars(t *testing.T) {
	t.Parallel()

	participants := []string{"+15551230001@cheogram.com", "+15551230002@cheogram.com"}
	got := groupRoomNameFromParticipants(context.Background(), nil, participants, "The O'Brien Family", "jmp")
	if got != "The O'Brien Family" {
		t.Fatalf("groupRoomNameFromParticipants() = %q, want %q", got, "The O'Brien Family")
	}
}

func TestSuffixSpecialChars(t *testing.T) {
	t.Parallel()

	if got := appendContactNameSuffix("O'Brien", "jmp"); got != "O'Brien (jmp)" {
		t.Fatalf("appendContactNameSuffix() = %q, want %q", got, "O'Brien (jmp)")
	}
	if got := appendContactNameSuffix("O'Brien (jmp)", "jmp"); got != "O'Brien (jmp)" {
		t.Fatalf("appendContactNameSuffix() = %q, want %q", got, "O'Brien (jmp)")
	}
	if got := stripContactNameSuffix("O'Brien (jmp)", "jmp"); got != "O'Brien" {
		t.Fatalf("stripContactNameSuffix() = %q, want %q", got, "O'Brien")
	}
}

func TestPortalLooksAutoNamedApostropheName(t *testing.T) {
	t.Parallel()

	portal := &bridgev2.Portal{
		Portal: &database.Portal{
			PortalKey:    networkid.PortalKey{ID: MakePortalID("+15551230001,+15551230002,+15551230003@cheogram.com")},
			Name:         "O'Brien (jmp)",
			RoomType:     database.RoomTypeDefault,
			NameIsCustom: true,
		},
	}
	if portalLooksAutoNamed(portal, "jmp") {
		t.Fatal("expected apostrophe name to be treated as explicit, not auto-named")
	}

	portal.Name = "OSR (jmp)"
	if !portalLooksAutoNamed(portal, "jmp") {
		t.Fatal("expected initials name to be treated as auto-named")
	}
}
