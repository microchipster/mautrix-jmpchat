package connector

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

const defaultContactNameSuffix = "jmp"

// SimpleConfig stores connector-specific network configuration.
type SimpleConfig struct {
	ContactNameSuffix string `yaml:"contact_name_suffix,omitempty"`
}

func (c *SimpleConfig) UnmarshalYAML(unmarshal func(any) error) error {
	type simpleConfigAlias SimpleConfig
	config := simpleConfigAlias{ContactNameSuffix: defaultContactNameSuffix}

	var scalar string
	if err := unmarshal(&scalar); err == nil {
		value := strings.TrimSpace(scalar)
		if value == "" {
			*c = SimpleConfig(config)
			return nil
		}
		if strings.ContainsAny(value, "/\\") || strings.HasSuffix(strings.ToLower(value), ".yaml") || strings.HasSuffix(strings.ToLower(value), ".yml") {
			data, err := os.ReadFile(value)
			if err != nil {
				return fmt.Errorf("read network config file %q: %w", value, err)
			}
			if err := yaml.Unmarshal(data, &config); err != nil {
				return fmt.Errorf("parse network config file %q: %w", value, err)
			}
		} else {
			config.ContactNameSuffix = value
		}
		*c = SimpleConfig(config)
		return nil
	}

	if err := unmarshal(&config); err != nil {
		return err
	}
	*c = SimpleConfig(config)
	return nil
}

func (c *MyConnector) contactNameSuffix() string {
	if c == nil || c.config == nil {
		return defaultContactNameSuffix
	}
	return strings.TrimSpace(c.config.ContactNameSuffix)
}

func (nc *MyNetworkClient) contactNameSuffix() string {
	if nc == nil || nc.connector == nil {
		return defaultContactNameSuffix
	}
	return nc.connector.contactNameSuffix()
}

func appendContactNameSuffix(name, suffix string) string {
	name = strings.TrimSpace(stripContactNameSuffix(name, suffix))
	suffix = strings.TrimSpace(suffix)
	if name == "" {
		return ""
	}
	if suffix == "" {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, suffix)
}

func stripContactNameSuffix(name, suffix string) string {
	name = strings.TrimSpace(name)
	suffix = strings.TrimSpace(suffix)
	if name == "" || suffix == "" {
		return name
	}
	pattern := " (" + suffix + ")"
	if strings.HasSuffix(name, pattern) {
		return strings.TrimSpace(strings.TrimSuffix(name, pattern))
	}
	return name
}

func contactDisplayName(ctx context.Context, bridge *bridgev2.Bridge, jid string) string {
	jid = bareJID(UnescapeJID(strings.TrimSpace(jid)))
	if jid == "" {
		return ""
	}
	if bridge != nil {
		ghost, err := bridge.GetExistingGhostByID(ctx, MakeUserID(jid))
		if err == nil && ghost != nil {
			if meta, ok := ghost.Metadata.(*GhostMetadata); ok && meta != nil {
				if name := strings.TrimSpace(meta.CustomName); name != "" {
					return name
				}
				if name := strings.TrimSpace(meta.RemoteName); name != "" {
					return name
				}
			}
			if name := strings.TrimSpace(ghost.Name); name != "" {
				return name
			}
		}
	}
	return contactLabelFromJID(jid)
}

func contactLabelFromJID(jid string) string {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return ""
	}
	if parts := strings.SplitN(jid, "/", 2); len(parts) == 2 && parts[1] != "" {
		if !strings.HasPrefix(parts[0], "+") {
			return parts[1]
		}
	}
	jid = bareJID(jid)
	if jid == "" {
		return ""
	}
	if parts := strings.SplitN(jid, "@", 2); len(parts) == 2 {
		if label := phoneSubscriberLabel(parts[0]); label != "" {
			return label
		}
		return parts[0]
	}
	if label := phoneSubscriberLabel(jid); label != "" {
		return label
	}
	return jid
}

func phoneSubscriberLabel(localpart string) string {
	localpart = stripPhoneURIParameters(strings.TrimSpace(localpart))
	if strings.HasPrefix(localpart, "+") {
		digits := strings.TrimPrefix(localpart, "+")
		if digits == "" {
			return ""
		}
		for _, r := range digits {
			if !unicode.IsDigit(r) {
				return ""
			}
		}
		return "+" + digits
	}
	// Also accept short codes (3-6 digits) as raw subscriber label
	if len(localpart) >= 3 && len(localpart) <= 6 {
		for _, r := range localpart {
			if !unicode.IsDigit(r) {
				return ""
			}
		}
		return localpart
	}
	return ""
}

func contactInitial(label string) string {
	for _, r := range strings.TrimSpace(label) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return strings.ToUpper(string(r))
		}
	}
	return ""
}

func groupInitialsFromLabels(labels []string) string {
	initials := make([]string, 0, len(labels))
	for _, label := range labels {
		if initial := contactInitial(label); initial != "" {
			initials = append(initials, initial)
		}
	}
	return strings.Join(initials, "")
}

func groupRoomNameFromParticipants(ctx context.Context, bridge *bridgev2.Bridge, participants []string, explicit, suffix string) string {
	if name := strings.TrimSpace(explicit); name != "" {
		return name
	}
	labels := make([]string, 0, len(participants))
	allNamed := len(participants) > 0
	for _, participant := range participants {
		label := contactDisplayName(ctx, bridge, participant)
		labels = append(labels, label)
		if phoneSubscriberLabel(strings.TrimSuffix(bareJID(participant), "@cheogram.com")) == label {
			allNamed = false
		}
	}
	name := strings.Join(labels, ",")
	if allNamed {
		name = groupInitialsFromLabels(labels)
	}
	if name == "" {
		name = "JMPChat Group"
	}
	return appendContactNameSuffix(name, suffix)
}

func groupRoomNameFromJID(ctx context.Context, bridge *bridgev2.Bridge, roomJID, explicit, suffix string) string {
	if name := strings.TrimSpace(explicit); name != "" {
		return name
	}
	contacts := splitCheogramGroupContacts(roomJID)
	if len(contacts) == 0 {
		roomJID = strings.TrimSpace(UnescapeJID(roomJID))
		if roomJID == "" {
			return ""
		}
		return roomJID
	}
	return groupRoomNameFromParticipants(ctx, bridge, contacts, "", suffix)
}

func portalUsesGroupChat(portal *bridgev2.Portal) bool {
	if portal == nil {
		return false
	}
	meta, _ := portal.Metadata.(*PortalMetadata)
	if meta != nil && meta.IsMUC {
		return true
	}
	return len(splitCheogramGroupContacts(UnescapeJID(string(portal.ID)))) > 0
}

func dmRoomName(ctx context.Context, bridge *bridgev2.Bridge, jid, explicit, suffix string) string {
	name := strings.TrimSpace(explicit)
	if name == "" {
		name = contactDisplayName(ctx, bridge, jid)
	}
	return appendContactNameSuffix(name, suffix)
}

func portalContacts(portal *bridgev2.Portal) []string {
	if portal == nil {
		return nil
	}
	if !portalUsesGroupChat(portal) {
		return nil
	}
	return splitCheogramGroupContacts(UnescapeJID(string(portal.ID)))
}

func portalParticipantJIDs(portal *bridgev2.Portal) []string {
	if portal == nil {
		return nil
	}
	if contacts := portalContacts(portal); len(contacts) > 0 {
		return contacts
	}
	target := strings.TrimSpace(string(portal.OtherUserID))
	if target == "" {
		target = UnescapeJID(string(portal.ID))
	}
	if target = CanonicalBareJID(target); target == "" {
		return nil
	}
	return []string{target}
}

func portalMatchesContact(portal *bridgev2.Portal, jid string) bool {
	if portal == nil {
		return false
	}
	jid = bareJID(UnescapeJID(strings.TrimSpace(jid)))
	if jid == "" {
		return false
	}
	if !portalUsesGroupChat(portal) {
		other := strings.TrimSpace(string(portal.OtherUserID))
		if other == "" {
			other = strings.TrimSpace(UnescapeJID(string(portal.ID)))
		}
		return strings.EqualFold(CanonicalBareJID(UnescapeJID(other)), jid)
	}
	for _, contact := range portalContacts(portal) {
		if strings.EqualFold(bareJID(contact), jid) {
			return true
		}
	}
	return false
}

func portalLooksAutoNamed(portal *bridgev2.Portal, suffix string) bool {
	if portal == nil {
		return false
	}
	if !portalUsesGroupChat(portal) {
		// DMs are auto-named if uncustomized or if current name matches auto-generated forms
		if !portal.NameIsCustom || portal.RoomType == database.RoomTypeDM {
			return true
		}
		name := stripContactNameSuffix(strings.TrimSpace(portal.Name), suffix)
		if name == "" || strings.Contains(name, "@") || strings.Contains(name, ";") {
			return true
		}
		// If the name is phone-like or shortcode, it was auto-named
		return phoneSubscriberLabel(name) != ""
	}
	name := stripContactNameSuffix(strings.TrimSpace(portal.Name), suffix)
	if name == "" {
		return true
	}
	if strings.Contains(name, "@cheogram.com") || strings.Contains(name, ";") {
		return true
	}
	contacts := portalContacts(portal)
	if len(contacts) == 0 {
		return false
	}
	phoneLabels := make([]string, 0, len(contacts))
	for _, contact := range contacts {
		phoneLabels = append(phoneLabels, contactLabelFromJID(contact))
	}
	if name == strings.Join(phoneLabels, ",") {
		return true
	}
	return strings.ToUpper(name) == name && utf8.RuneCountInString(name) == len(contacts)
}

func portalDesiredName(ctx context.Context, bridge *bridgev2.Bridge, portal *bridgev2.Portal, suffix string) string {
	if portal == nil {
		return ""
	}
	if portal.NameIsCustom && !portalLooksAutoNamed(portal, suffix) {
		if name := strings.TrimSpace(portal.Name); name != "" {
			return name
		}
	}
	if !portalUsesGroupChat(portal) {
		jid := strings.TrimSpace(string(portal.OtherUserID))
		if jid == "" {
			jid = strings.TrimSpace(UnescapeJID(string(portal.ID)))
		}
		return dmRoomName(ctx, bridge, jid, "", suffix)
	}
	if name := groupRoomNameFromJID(ctx, bridge, UnescapeJID(string(portal.ID)), "", suffix); name != "" {
		return name
	}
	if name := strings.TrimSpace(portal.Name); name != "" {
		return name
	}
	return UnescapeJID(string(portal.ID))
}

func queuePortalNameChange(ctx context.Context, bridge *bridgev2.Bridge, login *bridgev2.UserLogin, portal *bridgev2.Portal, name string) {
	if bridge == nil || login == nil || portal == nil || strings.TrimSpace(name) == "" {
		return
	}
	bridge.QueueRemoteEvent(login, &simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: networkid.PortalKey{ID: portal.ID},
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{ChatInfo: &bridgev2.ChatInfo{Name: &name}},
	})
}

func refreshAutoNamedPortals(ctx context.Context, bridge *bridgev2.Bridge, login *bridgev2.UserLogin, suffix string) (int, error) {
	return refreshAutoNamedPortalsMatching(ctx, bridge, login, suffix, nil)
}

func refreshContactNamedPortals(ctx context.Context, bridge *bridgev2.Bridge, login *bridgev2.UserLogin, contactJID, suffix string) error {
	_, err := refreshAutoNamedPortalsMatching(ctx, bridge, login, suffix, func(portal *bridgev2.Portal) bool {
		return portalMatchesContact(portal, contactJID)
	})
	return err
}

func refreshAutoNamedPortalsMatching(ctx context.Context, bridge *bridgev2.Bridge, login *bridgev2.UserLogin, suffix string, match func(*bridgev2.Portal) bool) (int, error) {
	if bridge == nil || login == nil {
		return 0, nil
	}
	userPortals, err := bridge.DB.UserPortal.GetAllForLogin(ctx, login.UserLogin)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, userPortal := range userPortals {
		portal, err := bridge.GetPortalByKey(ctx, userPortal.Portal)
		if err != nil || portal == nil || portal.MXID == "" {
			continue
		}
		matched := match == nil || match(portal)
		if !matched {
			continue
		}
		if match == nil && !portalLooksAutoNamed(portal, suffix) {
			continue
		}
		name := portalDesiredName(ctx, bridge, portal, suffix)
		if match != nil {
			name = portalGeneratedName(ctx, bridge, portal, suffix)
		}
		if name == "" || strings.TrimSpace(portal.Name) == name {
			continue
		}
		queuePortalNameChange(ctx, bridge, login, portal, name)
		count++
	}
	return count, nil
}

func portalGeneratedName(ctx context.Context, bridge *bridgev2.Bridge, portal *bridgev2.Portal, suffix string) string {
	if portal == nil {
		return ""
	}
	if portalUsesGroupChat(portal) {
		return groupRoomNameFromJID(ctx, bridge, UnescapeJID(string(portal.ID)), "", suffix)
	}
	jid := strings.TrimSpace(string(portal.OtherUserID))
	if jid == "" {
		jid = UnescapeJID(string(portal.ID))
	}
	return dmRoomName(ctx, bridge, jid, "", suffix)
}
