package connector

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

const defaultContactNameSuffix = "jmp"
const defaultCardDAVCountryCode = "1"
const groupNameSoftLimit = 28

// SimpleConfig stores connector-specific network configuration.
type SimpleConfig struct {
	ContactNameSuffix    string        `yaml:"contact_name_suffix,omitempty"`
	CardDAV              CardDAVConfig `yaml:"carddav,omitempty"`
	SendTyping           *bool         `yaml:"send_typing,omitempty"`
	SendReadMarkers      *bool         `yaml:"send_read_markers,omitempty"`
	SendDeliveryReceipts *bool         `yaml:"send_delivery_receipts,omitempty"`
	MUCNickSync          *bool         `yaml:"muc_nick_sync,omitempty"`
	DefaultServer        string        `yaml:"default_server,omitempty"`
	DefaultPort          int           `yaml:"default_port,omitempty"`
	ResourcePrefix       string        `yaml:"resource_prefix,omitempty"`
	HistoryDays          int           `yaml:"history_days,omitempty"`
	// HistoryTTL configures the Matrix-history retention cleanup.
	HistoryTTL HistoryTTLConfig `yaml:"history_ttl,omitempty"`
	Calls      CallsConfig      `yaml:"calls,omitempty"`
	Cdn        CdnConfig        `yaml:"cdn,omitempty"`
}

// sendTypingEnabled reports whether outgoing XEP-0085 chat states are allowed.
// The default is enabled.
func (c *SimpleConfig) sendTypingEnabled() bool {
	return c == nil || c.SendTyping == nil || *c.SendTyping
}

// sendReadMarkersEnabled reports whether outgoing XEP-0333 displayed markers
// (markable hints) are attached to messages. The default is enabled.
func (c *SimpleConfig) sendReadMarkersEnabled() bool {
	return c == nil || c.SendReadMarkers == nil || *c.SendReadMarkers
}

// sendDeliveryReceiptsEnabled reports whether XEP-0184 receipts are requested
// for outgoing direct messages. The default is enabled.
func (c *SimpleConfig) sendDeliveryReceiptsEnabled() bool {
	return c == nil || c.SendDeliveryReceipts == nil || *c.SendDeliveryReceipts
}

// mucNickSyncEnabled reports whether renaming a MUC portal room also changes
// the occupant nickname used in the XMPP room. The default is enabled.
func (c *SimpleConfig) mucNickSyncEnabled() bool {
	return c == nil || c.MUCNickSync == nil || *c.MUCNickSync
}

func (c *SimpleConfig) UnmarshalYAML(unmarshal func(any) error) error {
	type simpleConfigAlias SimpleConfig
	config := simpleConfigAlias{
		ContactNameSuffix: defaultContactNameSuffix,
		HistoryTTL:        DefaultHistoryTTLConfig(),
	}

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
		if config.CardDAV.DefaultCountryCode == "" {
			config.CardDAV.DefaultCountryCode = defaultCardDAVCountryCode
		}
		*c = SimpleConfig(config)
		return nil
	}

	if err := unmarshal(&config); err != nil {
		return err
	}
	if config.CardDAV.DefaultCountryCode == "" {
		config.CardDAV.DefaultCountryCode = defaultCardDAVCountryCode
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

// simpleConfig returns the connector's network configuration, if available.
func (nc *MyNetworkClient) simpleConfig() *SimpleConfig {
	if nc == nil || nc.connector == nil {
		return nil
	}
	return nc.connector.config
}

// historyLookback returns how far back MAM backfills should reach by default.
// It honours the optional history_days config key and falls back to the
// built-in default when it is not set.
func (nc *MyNetworkClient) historyLookback() time.Duration {
	if config := nc.simpleConfig(); config != nil && config.HistoryDays > 0 {
		return time.Duration(config.HistoryDays) * 24 * time.Hour
	}
	return defaultMAMLookback
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

// normalizeContactName cleans a display name coming from a user or an XMPP
// roster: runs of whitespace collapse to a single space, other control
// characters are dropped (formatting characters such as ZWJ are kept so emoji
// sequences survive), and surrounding whitespace is trimmed. Apostrophes,
// quotes, and non-ASCII letters are preserved exactly.
func normalizeContactName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(name))
	spacePending := false
	for _, r := range name {
		switch {
		case unicode.IsSpace(r):
			spacePending = b.Len() > 0
		case unicode.IsControl(r):
			// Drop control runes entirely.
		default:
			if spacePending {
				b.WriteRune(' ')
				spacePending = false
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// resolveGhostDisplayName resolves a contact display name from the layered
// name sources with precedence: explicit custom name, CardDAV name, XMPP
// roster name, the ghost's own name, then the JID/phone label.
func resolveGhostDisplayName(meta *GhostMetadata, ghostName, jid string) string {
	if meta != nil {
		if name := strings.TrimSpace(meta.CustomName); name != "" {
			return name
		}
		if name := strings.TrimSpace(cardDAVDecryptStoredName(meta.CardDAVName)); name != "" {
			return name
		}
		if name := strings.TrimSpace(meta.RemoteName); name != "" {
			return name
		}
	}
	if name := strings.TrimSpace(ghostName); name != "" {
		return name
	}
	return contactLabelFromJID(jid)
}

func contactDisplayName(ctx context.Context, bridge *bridgev2.Bridge, jid string) string {
	jid = bareJID(UnescapeJID(strings.TrimSpace(jid)))
	if jid == "" {
		return ""
	}
	if bridge != nil {
		ghost, err := bridge.GetExistingGhostByID(ctx, MakeUserID(jid))
		if err == nil && ghost != nil {
			var meta *GhostMetadata
			if m, ok := ghost.Metadata.(*GhostMetadata); ok {
				meta = m
			}
			if name := resolveGhostDisplayName(meta, ghost.Name, jid); name != "" {
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

// nonEmptyLabels returns the labels that carry usable text.
func nonEmptyLabels(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		if strings.TrimSpace(label) != "" {
			out = append(out, label)
		}
	}
	return out
}

// initialsLikeName reports whether name looks like the initials generated for a
// group with the given number of contacts. Names shorter than the number of
// contacts (for example because a participant had no display name yet when the
// name was generated) still count as auto-generated so that they are
// regenerated once names are known.
func initialsLikeName(name string, contacts int) bool {
	if name == "" || contacts <= 0 {
		return false
	}
	if strings.ToUpper(name) != name {
		return false
	}
	count := 0
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
		count++
	}
	return count >= 1 && count <= contacts
}

func groupRoomNameFromParticipants(ctx context.Context, bridge *bridgev2.Bridge, participants []string, explicit, suffix, ownInitial string) string {
	if name := strings.TrimSpace(explicit); name != "" {
		return name
	}
	if len(participants) == 0 {
		return appendContactNameSuffix("JMPChat Group", suffix)
	}
	labels := make([]string, len(participants))
	names := make([]string, len(participants))
	allNamed := true
	for index, participant := range participants {
		label := strings.TrimSpace(contactDisplayName(ctx, bridge, participant))
		labels[index] = label
		name := label
		if name == "" {
			name = participant
		}
		names[index] = name
		if label == "" {
			allNamed = false
			continue
		}
		if phoneSubscriberLabel(strings.TrimSuffix(bareJID(participant), "@cheogram.com")) == label {
			allNamed = false
		}
	}
	full := strings.Join(nonEmptyLabels(names), ",")
	if len([]rune(full)) <= groupNameSoftLimit {
		return appendContactNameSuffix(full, suffix)
	}
	firsts := make([]string, len(names))
	for index, name := range names {
		firsts[index] = firstWord(name)
	}
	firstsList := strings.Join(nonEmptyLabels(firsts), ",")
	if firstsList != full && len([]rune(firstsList)) <= groupNameSoftLimit {
		return appendContactNameSuffix(firstsList, suffix)
	}
	if allNamed {
		if initials := groupInitialsFromLabels(labels) + ownInitial; initials != "" {
			return appendContactNameSuffix(initials, suffix)
		}
	}
	name := jmpFitLabelList(names)
	if name == "" {
		name = "JMPChat Group"
	}
	return appendContactNameSuffix(name, suffix)
}

// firstWord returns the first whitespace-delimited word of a label.
func firstWord(label string) string {
	if index := strings.IndexAny(label, " \t"); index > 0 {
		return label[:index]
	}
	return label
}

// jmpFitLabelList fits as many labels as possible into groupNameSoftLimit and
// collapses the remainder into ", and N more".
func jmpFitLabelList(labels []string) string {
	nonEmpty := nonEmptyLabels(labels)
	if len(nonEmpty) == 0 {
		return ""
	}
	fitted := 0
	length := 0
	for index, label := range nonEmpty {
		added := len([]rune(label))
		if index > 0 {
			added += 1 // ","
		}
		if fitted > 0 && length+added > groupNameSoftLimit {
			break
		}
		length += added
		fitted++
	}
	if fitted >= len(nonEmpty) {
		return strings.Join(nonEmpty, ",")
	}
	return strings.Join(nonEmpty[:fitted], ",") +
		", and " + strconv.Itoa(len(nonEmpty)-fitted) + " more"
}

func groupRoomNameFromJID(ctx context.Context, bridge *bridgev2.Bridge, roomJID, explicit, suffix, ownInitial string) string {
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
	return groupRoomNameFromParticipants(ctx, bridge, contacts, "", suffix, ownInitial)
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

// outboundXMPPStanzaType chooses the XMPP message type for stanzas the bridge
// sends to a portal. Cheogram gateway JIDs are not MUCs and expect type=chat,
// even when their local part is a comma-separated group text list. Real MUC
// rooms keep type=groupchat. This is the single decision point for outbound
// message stanza types (messages, reactions, edits, retractions, typing,
// receipts and MUC subject changes).
func outboundXMPPStanzaType(portal *bridgev2.Portal) stanza.StanzaType {
	if portal == nil {
		return stanza.MessageTypeChat
	}
	if isCheogramGatewayJID(UnescapeJID(string(portal.ID))) {
		return stanza.MessageTypeChat
	}
	if portalUsesGroupChat(portal) {
		return stanza.MessageTypeGroupchat
	}
	return stanza.MessageTypeChat
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
	if portal != nil {
		if meta, _ := portal.Metadata.(*PortalMetadata); meta != nil && meta.AutoName != "" && strings.TrimSpace(stripContactNameSuffix(portal.Name, suffix)) == meta.AutoName {
			return true
		}
	}
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
	if nameTokensLookGenerated(name, phoneLabels) {
		return true
	}
	return initialsLikeName(name, len(contacts)+1)
}

func portalDesiredName(ctx context.Context, bridge *bridgev2.Bridge, portal *bridgev2.Portal, suffix string, ownInitial string) string {
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
	if name := groupRoomNameFromJID(ctx, bridge, UnescapeJID(string(portal.ID)), "", suffix, ownInitial); name != "" {
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

// refreshAllPortalNames regenerates portal names from contact data even for
// rooms whose names were customized by hand. Used by carddav-sync with
// --force-names; returns how many rooms were queued for renaming.
func refreshAllPortalNames(ctx context.Context, bridge *bridgev2.Bridge, login *bridgev2.UserLogin, suffix string) (int, error) {
	return refreshAutoNamedPortalsMatching(ctx, bridge, login, suffix, func(*bridgev2.Portal) bool { return true })
}

func refreshAutoNamedPortalsMatching(ctx context.Context, bridge *bridgev2.Bridge, login *bridgev2.UserLogin, suffix string, match func(*bridgev2.Portal) bool) (int, error) {
	ownInitial := ""
	if login != nil {
		if client, ok := login.Client.(*MyNetworkClient); ok {
			ownInitial = client.ownInitial(ctx)
		}
	}
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
		name := portalDesiredName(ctx, bridge, portal, suffix, ownInitial)
		if match != nil {
			name = portalGeneratedName(ctx, bridge, portal, suffix, ownInitial)
		}
		if name == "" {
			continue
		}
		meta, _ := portal.Metadata.(*PortalMetadata)
		generated := strings.TrimSpace(stripContactNameSuffix(name, suffix))
		if strings.TrimSpace(portal.Name) == name {
			if meta != nil && meta.AutoName == "" {
				rememberAutoName(ctx, portal, meta, generated)
			}
			continue
		}
		queuePortalNameChange(ctx, bridge, login, portal, name)
		if meta != nil {
			rememberAutoName(ctx, portal, meta, generated)
		}
		count++
	}
	return count, nil
}

func portalGeneratedName(ctx context.Context, bridge *bridgev2.Bridge, portal *bridgev2.Portal, suffix string, ownInitial string) string {
	if portal == nil {
		return ""
	}
	if portalUsesGroupChat(portal) {
		return groupRoomNameFromJID(ctx, bridge, UnescapeJID(string(portal.ID)), "", suffix, ownInitial)
	}
	jid := strings.TrimSpace(string(portal.OtherUserID))
	if jid == "" {
		jid = UnescapeJID(string(portal.ID))
	}
	return dmRoomName(ctx, bridge, jid, "", suffix)
}

// ownInitial returns the initial of the bridge owner's own name: the contact
// name of the bridge's own JID when known, else the Matrix display name of
// the account. The value is cached briefly so refreshes do not refetch it.
func (nc *MyNetworkClient) ownInitial(ctx context.Context) string {
	if nc == nil || nc.bridge == nil {
		return ""
	}
	nc.ownInitialMu.Lock()
	if !nc.ownInitialAt.IsZero() && time.Since(nc.ownInitialAt) < 5*time.Minute {
		initial := nc.ownInitialCache
		nc.ownInitialMu.Unlock()
		return initial
	}
	nc.ownInitialMu.Unlock()

	initial := ""
	if own := nc.ownBareJID(); own != "" {
		label := contactDisplayName(ctx, nc.bridge, own)
		phone := phoneSubscriberLabel(strings.TrimSuffix(bareJID(own), "@cheogram.com"))
		if label != "" && label != phone {
			initial = contactInitial(label)
		}
	}
	if initial == "" && nc.login != nil && nc.login.User != nil {
		if matrixConnector, ok := nc.bridge.Matrix.(*matrix.Connector); ok && matrixConnector.Bot != nil {
			if profile, err := matrixConnector.Bot.GetProfile(ctx, nc.login.User.MXID); err == nil && profile != nil {
				initial = contactInitial(profile.DisplayName)
			}
		}
	}
	nc.ownInitialMu.Lock()
	nc.ownInitialCache = initial
	nc.ownInitialAt = time.Now()
	nc.ownInitialMu.Unlock()
	return initial
}

// nameTokensLookGenerated reports whether a room name looks like one the
// bridge generated: every comma-separated token matches a known label (or
// number), or at least two of them do while the rest are stale labels from
// an earlier contact set.
func nameTokensLookGenerated(name string, known []string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	if index := strings.Index(trimmed, ", and "); index >= 0 && strings.HasSuffix(trimmed, " more") {
		trimmed = trimmed[:index]
	}
	tokens := 0
	matches := 0
	for _, token := range strings.Split(trimmed, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		tokens++
		for _, candidate := range known {
			if candidate != "" && strings.EqualFold(candidate, token) {
				matches++
				break
			}
		}
	}
	if tokens == 0 {
		return false
	}
	if matches == tokens {
		return true
	}
	return tokens >= 2 && matches >= 2
}

// rememberAutoName stores the generated room name on the portal metadata so a
// later contact change still recognises the room as auto-named.
func rememberAutoName(ctx context.Context, portal *bridgev2.Portal, meta *PortalMetadata, generated string) {
	if portal == nil || meta == nil || generated == "" || meta.AutoName == generated {
		return
	}
	meta.AutoName = generated
	portal.Metadata = meta
	if err := portal.Save(ctx); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("portal_id", string(portal.ID)).Msg("Failed to remember the generated room name")
	}
}
