package connector

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
		return parts[1]
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
	localpart = strings.TrimSpace(localpart)
	if !strings.HasPrefix(localpart, "+") {
		return ""
	}
	digits := strings.TrimPrefix(localpart, "+")
	if digits == "" {
		return ""
	}
	for _, r := range digits {
		if !unicode.IsDigit(r) {
			return ""
		}
	}
	if len(digits) > 10 {
		digits = digits[len(digits)-10:]
	}
	return digits
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
	for _, participant := range participants {
		labels = append(labels, contactDisplayName(ctx, bridge, participant))
	}
	name := groupInitialsFromLabels(labels)
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
	meta, _ := portal.Metadata.(*PortalMetadata)
	if meta == nil || !meta.IsMUC {
		return nil
	}
	return splitCheogramGroupContacts(UnescapeJID(string(portal.ID)))
}

func portalMatchesContact(portal *bridgev2.Portal, jid string) bool {
	if portal == nil {
		return false
	}
	jid = bareJID(UnescapeJID(strings.TrimSpace(jid)))
	if jid == "" {
		return false
	}
	meta, _ := portal.Metadata.(*PortalMetadata)
	if portal.RoomType == database.RoomTypeDM || (meta != nil && !meta.IsMUC) {
		other := strings.TrimSpace(string(portal.OtherUserID))
		if other == "" {
			other = strings.TrimSpace(UnescapeJID(string(portal.ID)))
		}
		return strings.EqualFold(bareJID(other), jid)
	}
	if meta == nil {
		return false
	}
	for _, contact := range portalContacts(portal) {
		if strings.EqualFold(bareJID(contact), jid) {
			return true
		}
	}
	return false
}

func portalLooksAutoNamed(portal *bridgev2.Portal, suffix string) bool {
	if portal == nil || portal.NameIsCustom {
		return false
	}
	if portal.RoomType == database.RoomTypeDM {
		return true
	}
	meta, _ := portal.Metadata.(*PortalMetadata)
	if meta == nil || !meta.IsMUC {
		return false
	}
	name := stripContactNameSuffix(strings.TrimSpace(portal.Name), suffix)
	if name == "" {
		return true
	}
	if strings.Contains(name, "@cheogram.com") {
		return true
	}
	contacts := portalContacts(portal)
	if len(contacts) == 0 {
		return false
	}
	return strings.ToUpper(name) == name && utf8.RuneCountInString(name) == len(contacts)
}

func portalDesiredName(ctx context.Context, bridge *bridgev2.Bridge, portal *bridgev2.Portal, suffix string) string {
	if portal == nil {
		return ""
	}
	if portal.NameIsCustom {
		if name := strings.TrimSpace(portal.Name); name != "" {
			return name
		}
	}
	meta, _ := portal.Metadata.(*PortalMetadata)
	if portal.RoomType == database.RoomTypeDM || (meta != nil && !meta.IsMUC) {
		jid := strings.TrimSpace(string(portal.OtherUserID))
		if jid == "" {
			jid = strings.TrimSpace(UnescapeJID(string(portal.ID)))
		}
		return dmRoomName(ctx, bridge, jid, "", suffix)
	}
	if meta != nil && meta.IsMUC {
		if name := groupRoomNameFromJID(ctx, bridge, UnescapeJID(string(portal.ID)), "", suffix); name != "" {
			return name
		}
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
		if match != nil && !match(portal) {
			continue
		}
		if !portalLooksAutoNamed(portal, suffix) {
			continue
		}
		name := portalDesiredName(ctx, bridge, portal, suffix)
		if name == "" || strings.TrimSpace(portal.Name) == name {
			continue
		}
		queuePortalNameChange(ctx, bridge, login, portal, name)
		count++
	}
	return count, nil
}
