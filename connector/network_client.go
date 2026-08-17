package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"gosrc.io/xmpp"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type CarbonsEnable struct {
	XMLName xml.Name `xml:"urn:xmpp:carbons:2 enable"`
}

type XMPPPing struct {
	XMLName xml.Name `xml:"urn:xmpp:ping ping"`
}

type loginConnectionStatus struct {
	Connected          bool
	SessionEstablished bool
	BindJID            string
	Uptime             time.Duration
	ReconnectAttempt   int
}

type syncStats struct {
	dmPortals           map[string]bool
	mucBookmarkPortals  map[string]bool
	mucDiscoPortals     map[string]bool
	mamDMBackfills      int
	mamMUCBackfills     int
	mucHistoryFallbacks int
	lastErrors          []string
	errorsTruncated     bool
}

type pendingMUCCreation struct {
	name         string
	participants []string
}

func (c *CarbonsEnable) Namespace() string {
	return "urn:xmpp:carbons:2"
}

func (c *CarbonsEnable) GetSet() *stanza.ResultSet {
	return nil
}

func (p *XMPPPing) Namespace() string {
	return "urn:xmpp:ping"
}

func (p *XMPPPing) GetSet() *stanza.ResultSet {
	return nil
}

// Ensure MyNetworkClient implements NetworkAPI.
var _ bridgev2.NetworkAPI = (*MyNetworkClient)(nil)

// MyNetworkClient implements the bridgev2.NetworkAPI for interacting
// with the simple network on behalf of a specific user login.
type MyNetworkClient struct {
	log       zerolog.Logger
	bridge    *bridgev2.Bridge
	login     *bridgev2.UserLogin
	connector *MyConnector

	xmppClient                   *xmpp.Client
	xmppRouter                   *xmpp.Router
	httpUploadServiceJID         string
	mucRooms                     map[string]bool
	mucJoined                    map[string]bool
	mucOccupants                 map[string]map[string]string
	mucNick                      map[string]string
	mucPasswords                 map[string]string
	mucPendingCreate             map[string]pendingMUCCreation
	mucBackfillPending           map[string]time.Time
	mucBackfillMu                sync.Mutex
	mucServices                  map[string]bool
	mucServiceDiscoRequested     map[string]bool
	mucMAMDiscoRequested         map[string]bool
	mucHistoryRequests           map[string]stanza.History
	mucHistoryFallbackAttempts   map[string]bool
	mucHistoryMu                 sync.Mutex
	discoFeatures                map[string]map[string]bool
	discoMu                      sync.Mutex
	mamSupportMu                 sync.Mutex
	mamServerUnsupportedNotified bool
	mamMUCUnsupportedNotified    map[string]bool
	mamMUCUnsupportedPendingSync map[string]bool
	dmBackfillPending            map[string]time.Time
	rosterItems                  map[string]bool
	bookmarkedRooms              map[string]bool
	fullSyncRequested            bool
	fullSyncRosterPending        bool
	fullSyncBookmarksPending     bool
	fullSyncAccountDiscoPending  bool
	fullSyncBackfillsPending     int
	fullSyncStart                time.Time
	fullSyncUseStart             bool
	sessionEstablishedAt         time.Time
	rosterMu                     sync.RWMutex
	mamQueryMu                   sync.Mutex
	mamQueryResults              map[string][]collectedMAMMessage
	callSessionsMu               sync.Mutex
	callSessions                 map[string]*xmppCallSessionState
	connected                    bool
	connectedCh                  chan struct{}
	syncMu                       sync.Mutex
	syncRunning                  bool
	syncStatus                   string
	syncLastErr                  string
	syncStartedAt                time.Time
	syncStats                    syncStats
	syncStatsMu                  sync.Mutex
	connectMu                    sync.Mutex
	connectStarted               bool
	stopReconnect                chan struct{}
	reconnectAttempt             int
	postConnectMu                sync.Mutex
	bridgeStateMu                sync.Mutex
	lastBridgeState              status.BridgeStateEvent
}

const defaultMAMLookback = 30 * 24 * time.Hour

const (
	xmppReconnectInitialBackoff = 5 * time.Second
	xmppReconnectMaxBackoff     = 2 * time.Minute
	xmppHealthCheckInterval     = time.Minute
)

func xmppReconnectBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	backoff := xmppReconnectInitialBackoff
	for i := 1; i < attempt; i++ {
		if backoff >= xmppReconnectMaxBackoff {
			return xmppReconnectMaxBackoff
		}
		backoff *= 2
		if backoff >= xmppReconnectMaxBackoff {
			return xmppReconnectMaxBackoff
		}
	}
	return backoff
}

func (nc *MyNetworkClient) tryStartConnect() (attempt int, started bool) {
	nc.connectMu.Lock()
	defer nc.connectMu.Unlock()
	if nc.connectStarted {
		attempt = nc.reconnectAttempt
		if attempt == 0 {
			attempt = 1
		}
		return attempt, false
	}
	nc.connectStarted = true
	if nc.stopReconnect == nil {
		nc.stopReconnect = make(chan struct{})
	}
	attempt = nc.reconnectAttempt
	if attempt == 0 {
		attempt = 1
	}
	return attempt, true
}

func (nc *MyNetworkClient) releaseConnect() {
	nc.connectMu.Lock()
	nc.connectStarted = false
	nc.connectMu.Unlock()
}

func (nc *MyNetworkClient) connectionJID() string {
	if nc.login == nil {
		return ""
	}
	if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil {
		return meta.RemoteUserID
	}
	return ""
}

func (nc *MyNetworkClient) defaultMUCNick() string {
	meta, ok := nc.login.Metadata.(*LoginMetadata)
	if ok && meta != nil {
		if parts := strings.Split(meta.RemoteUserID, "@"); len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}
	if nc.login.RemoteName != "" {
		return nc.login.RemoteName
	}
	return "matrix"
}

func bareJID(jid string) string {
	return strings.SplitN(jid, "/", 2)[0]
}

func (nc *MyNetworkClient) ownBareJID() string {
	if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil {
		return bareJID(meta.RemoteUserID)
	}
	return ""
}

func (nc *MyNetworkClient) queuePendingMUCCreation(roomJID string, pending pendingMUCCreation) {
	if nc.mucPendingCreate == nil {
		nc.mucPendingCreate = map[string]pendingMUCCreation{}
	}
	nc.mucPendingCreate[roomJID] = pending
}

func (nc *MyNetworkClient) popPendingMUCCreation(roomJID string) (pendingMUCCreation, bool) {
	if nc.mucPendingCreate == nil {
		return pendingMUCCreation{}, false
	}
	pending, ok := nc.mucPendingCreate[roomJID]
	if ok {
		delete(nc.mucPendingCreate, roomJID)
	}
	return pending, ok
}

func mucHasStatus(mucUser *MUCUserInvite, code string) bool {
	if mucUser == nil {
		return false
	}
	for _, status := range mucUser.Status {
		if status.Code == code {
			return true
		}
	}
	return false
}

func (nc *MyNetworkClient) isSelfMUCPresence(roomJID, resource string, mucUser *MUCUserInvite) bool {
	selfNick := nc.mucNick[roomJID]
	if mucHasStatus(mucUser, "110") {
		return true
	}
	if mucUser != nil && mucUser.Item != nil {
		if itemJID := bareJID(strings.TrimSpace(mucUser.Item.JID)); itemJID != "" && itemJID == nc.ownBareJID() {
			return true
		}
		if selfNick != "" && strings.TrimSpace(mucUser.Item.Nick) == selfNick {
			return true
		}
	}
	return selfNick != "" && resource == selfNick
}

func (nc *MyNetworkClient) resolveMUCPresenceMemberJID(roomJID string, pres stanza.Presence, resource string, mucUser *MUCUserInvite) string {
	if mucUser != nil && mucUser.Item != nil {
		if jid := bareJID(strings.TrimSpace(mucUser.Item.JID)); jid != "" {
			return jid
		}
		if nick := strings.TrimSpace(mucUser.Item.Nick); nick != "" {
			resource = nick
		}
	}
	if strings.Contains(roomJID, ",") && strings.HasSuffix(roomJID, "@cheogram.com") {
		if strings.HasPrefix(resource, "+") && !strings.Contains(resource, "@") {
			return resource + "@cheogram.com"
		}
	}
	if resource != "" {
		return roomJID + "/" + resource
	}
	if pres.From != "" {
		return pres.From
	}
	return roomJID
}

func (nc *MyNetworkClient) requestBookmarks() error {
	if err := nc.requestBookmarks2(); err != nil {
		nc.log.Err(err).Msg("Failed to send bookmarks2 request")
	}
	bookmarksIq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		Id:   fmt.Sprintf("bookmarks_%d", time.Now().UnixNano()),
	})
	bookmarksIq.Payload = &PrivateBookmarkQuery{Storage: &BookmarkStorage{}}
	if err := nc.sendXMPP(bookmarksIq); err != nil {
		return err
	}
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("id", bookmarksIq.Id).
			Msg("Sent private bookmarks request")
	}
	return nil
}

func (nc *MyNetworkClient) requestBookmarks2() error {
	remote := ""
	if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil {
		remote = meta.RemoteUserID
	}
	if remote == "" {
		return fmt.Errorf("remote user id missing")
	}
	bare := strings.Split(remote, "/")[0]
	if bare == "" {
		return fmt.Errorf("remote user id missing bare jid")
	}
	bookmarksIq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		Id:   fmt.Sprintf("bookmarks2_%d", time.Now().UnixNano()),
		To:   bare,
	})
	bookmarksIq.Payload = &stanza.PubSubGeneric{
		Items: &stanza.Items{Node: NSBookmarks2},
	}
	if err := nc.sendXMPP(bookmarksIq); err != nil {
		return err
	}
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("id", bookmarksIq.Id).
			Str("to", bare).
			Str("node", NSBookmarks2).
			Msg("Sent bookmarks2 request")
	}
	return nil
}

func (nc *MyNetworkClient) requestRoster() error {
	iq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		Id:   fmt.Sprintf("roster_%d", time.Now().UnixNano()),
	})
	iq.Payload = &stanza.Roster{}
	if err := nc.sendXMPP(iq); err != nil {
		return err
	}
	return nil
}

func (nc *MyNetworkClient) requestOwnDiscoInfo() {
	bare := nc.ownBareJID()
	if bare == "" {
		return
	}
	nc.requestDiscoInfoAsync(bare, "disco_info_account")
}

func (nc *MyNetworkClient) scheduleDMBackfill(withJID string, start time.Time) {
	withJID = bareJID(strings.TrimSpace(withJID))
	if withJID == "" {
		withJID = "*"
	}
	nc.syncMu.Lock()
	if nc.dmBackfillPending == nil {
		nc.dmBackfillPending = map[string]time.Time{}
	}
	nc.dmBackfillPending[withJID] = start
	fullSyncRosterPending := nc.fullSyncRequested && nc.fullSyncRosterPending
	nc.syncMu.Unlock()
	if fullSyncRosterPending {
		return
	}
	nc.maybeStartPendingDMBackfills()
}

func (nc *MyNetworkClient) maybeStartPendingDMBackfills() {
	if !nc.isXMPPConnected() {
		return
	}
	supported, known := nc.mamSupportedForServer()
	if !known {
		nc.requestOwnDiscoInfo()
		return
	}

	nc.syncMu.Lock()
	if nc.fullSyncAccountDiscoPending {
		nc.fullSyncAccountDiscoPending = false
	}
	if nc.fullSyncRequested && nc.fullSyncRosterPending {
		nc.syncMu.Unlock()
		nc.maybeClearFullSync()
		return
	}
	pending := nc.dmBackfillPending
	nc.dmBackfillPending = map[string]time.Time{}
	nc.syncMu.Unlock()

	if len(pending) == 0 {
		nc.maybeClearFullSync()
		return
	}
	if !supported {
		nc.ensureMAMSupportForServer()
		nc.maybeClearFullSync()
		return
	}

	start := time.Time{}
	for _, candidate := range pending {
		if start.IsZero() || (!candidate.IsZero() && candidate.Before(start)) {
			start = candidate
		}
	}
	nc.recordMAMBackfillDM()
	tracked := nc.trackFullSyncBackfill()
	go func(start time.Time, tracked bool) {
		defer nc.finishTrackedFullSyncBackfill(tracked)
		if err := nc.backfillAccountDMs(context.Background(), start); err != nil {
			if isServiceUnavailableMAMError(err) {
				nc.log.Info().Time("start", start).Msg("Skipping account DM MAM history; server returned service-unavailable")
			} else {
				nc.log.Err(err).Time("start", start).Msg("Failed to request account DM MAM history")
				if tracked {
					nc.reportSyncError(fmt.Sprintf("DM backfill failed: %v", err))
				} else {
					nc.log.Debug().Err(err).Time("start", start).Msg("Automatic DM backfill failed")
				}
			}
		}
	}(start, tracked)
	for jid := range pending {
		if jid == "*" {
			continue
		}
		nc.recordDMPortal(string(MakePortalID(jid)))
	}
}

func (nc *MyNetworkClient) joinMUC(roomJID, nickname, password string) {
	if nickname == "" {
		nickname = nc.defaultMUCNick()
	}
	fullJID := roomJID + "/" + nickname

	pres := stanza.NewPresence(stanza.Attrs{To: fullJID})
	history := stanza.History{MaxChars: stanza.NewNullableInt(0)}
	if fallback, ok := nc.popMUCHistoryRequest(roomJID); ok {
		history = fallback
	}
	historyDisabled := false
	if maxChars, ok := history.MaxChars.Get(); ok && maxChars == 0 {
		_, maxStanzasSet := history.MaxStanzas.Get()
		_, secondsSet := history.Seconds.Get()
		if history.Since.IsZero() && !maxStanzasSet && !secondsSet {
			historyDisabled = true
		}
	}
	maxChars, maxCharsSet := history.MaxChars.Get()
	maxStanzas, maxStanzasSet := history.MaxStanzas.Get()
	seconds, secondsSet := history.Seconds.Get()
	pres.Extensions = append(pres.Extensions, stanza.MucPresence{
		Password: password,
		History:  history,
	})
	if nc.mucNick == nil {
		nc.mucNick = map[string]string{}
	}
	nc.mucNick[roomJID] = nickname
	if nc.mucPasswords == nil {
		nc.mucPasswords = map[string]string{}
	}
	nc.mucPasswords[roomJID] = password
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("room_jid", roomJID).
			Str("nick", nickname).
			Bool("history_disabled", historyDisabled).
			Time("history_since", history.Since).
			Int("history_maxchars", maxChars).
			Bool("history_maxchars_set", maxCharsSet).
			Int("history_maxstanzas", maxStanzas).
			Bool("history_maxstanzas_set", maxStanzasSet).
			Int("history_seconds", seconds).
			Bool("history_seconds_set", secondsSet).
			Bool("has_password", password != "").
			Msg("Joining MUC")
	}
	if nc.mucRooms != nil {
		nc.mucRooms[roomJID] = true
	}

	if err := nc.sendXMPP(pres); err != nil {
		nc.log.Err(err).Str("room_jid", roomJID).Str("nick", nickname).Msg("Failed to join MUC")
		return
	}

	nc.log.Info().Str("room_jid", roomJID).Str("nick", nickname).Msg("Sent MUC join presence")
	nc.maybeStartPendingMUCBackfill(roomJID)
}

func (nc *MyNetworkClient) scheduleMUCBackfill(roomJID string, start time.Time) {
	supported, known := nc.mamSupportedForMUC(roomJID)
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("room_jid", roomJID).
			Str("service", jidDomain(roomJID)).
			Bool("mam_supported", supported).
			Bool("mam_known", known).
			Time("start", start).
			Msg("Evaluating MUC MAM support")
	}
	nc.mucBackfillMu.Lock()
	if nc.mucBackfillPending == nil {
		nc.mucBackfillPending = map[string]time.Time{}
	}
	nc.mucBackfillPending[roomJID] = start
	nc.mucBackfillMu.Unlock()
	if !known {
		nc.requestMUCMAMSupportInfo(roomJID)
		if nc.log.GetLevel() <= zerolog.DebugLevel {
			nc.log.Debug().
				Str("room_jid", roomJID).
				Time("start", start).
				Msg("MUC MAM support unknown; waiting for disco info")
		}
		return
	}
	if !supported {
		nc.popMUCBackfill(roomJID)
		_ = nc.ensureMAMSupportForMUC(roomJID)
		nc.scheduleMUCHistoryFallback(roomJID, start)
		return
	}
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("room_jid", roomJID).
			Time("start", start).
			Bool("start_zero", start.IsZero()).
			Msg("Scheduled MUC MAM backfill")
	}
	nc.maybeStartPendingMUCBackfill(roomJID)
}

func (nc *MyNetworkClient) popMUCBackfill(roomJID string) (time.Time, bool) {
	nc.mucBackfillMu.Lock()
	defer nc.mucBackfillMu.Unlock()
	if nc.mucBackfillPending == nil {
		return time.Time{}, false
	}
	start, ok := nc.mucBackfillPending[roomJID]
	if ok {
		delete(nc.mucBackfillPending, roomJID)
	}
	return start, ok
}

func (nc *MyNetworkClient) requestDiscoInfoAsync(jid string, prefix string) {
	jid = strings.TrimSpace(jid)
	if jid == "" || !nc.isXMPPConnected() {
		return
	}
	nc.discoMu.Lock()
	if nc.mucMAMDiscoRequested == nil {
		nc.mucMAMDiscoRequested = map[string]bool{}
	}
	if nc.discoFeatures != nil {
		if _, ok := nc.discoFeatures[jid]; ok {
			nc.discoMu.Unlock()
			return
		}
	}
	if nc.mucMAMDiscoRequested[jid] {
		nc.discoMu.Unlock()
		return
	}
	nc.mucMAMDiscoRequested[jid] = true
	nc.discoMu.Unlock()
	iq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		To:   jid,
		Id:   fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano()),
	})
	iq.Payload = &stanza.DiscoInfo{}
	if err := nc.sendXMPP(iq); err != nil {
		nc.discoMu.Lock()
		delete(nc.mucMAMDiscoRequested, jid)
		nc.discoMu.Unlock()
		nc.log.Err(err).Str("to", jid).Msg("Failed to request disco info")
	}
}

func (nc *MyNetworkClient) requestMUCMAMSupportInfo(roomJID string) {
	nc.requestDiscoInfoAsync(roomJID, "disco_info_room")
	service := jidDomain(roomJID)
	if service != "" && service != roomJID {
		nc.requestDiscoInfoAsync(service, "disco_info_service")
	}
}

func (nc *MyNetworkClient) maybeStartPendingMUCBackfill(roomJID string) {
	if roomJID == "" || !nc.isXMPPConnected() {
		return
	}
	if nc.mucRooms == nil || !nc.mucRooms[roomJID] {
		return
	}
	if nc.mucJoined == nil || !nc.mucJoined[roomJID] {
		return
	}
	start, ok := nc.popMUCBackfill(roomJID)
	if !ok {
		return
	}
	supported, known := nc.mamSupportedForMUC(roomJID)
	if !known {
		nc.mucBackfillMu.Lock()
		if nc.mucBackfillPending == nil {
			nc.mucBackfillPending = map[string]time.Time{}
		}
		nc.mucBackfillPending[roomJID] = start
		nc.mucBackfillMu.Unlock()
		nc.requestMUCMAMSupportInfo(roomJID)
		return
	}
	if !supported {
		_ = nc.ensureMAMSupportForMUC(roomJID)
		nc.scheduleMUCHistoryFallback(roomJID, start)
		nick := nc.defaultMUCNick()
		if nc.mucNick != nil && nc.mucNick[roomJID] != "" {
			nick = nc.mucNick[roomJID]
		}
		password := ""
		if nc.mucPasswords != nil {
			password = nc.mucPasswords[roomJID]
		}
		nc.joinMUC(roomJID, nick, password)
		return
	}
	nc.recordMAMBackfillMUC()
	tracked := nc.trackFullSyncBackfill()
	go func(roomJID string, start time.Time) {
		defer nc.finishTrackedFullSyncBackfill(tracked)
		if err := nc.backfillMUC(context.Background(), roomJID, start); err != nil {
			if isServiceUnavailableMAMError(err) {
				nc.log.Info().Str("room_jid", roomJID).Msg("Skipping MUC MAM history; server returned service-unavailable")
			} else {
				nc.log.Err(err).Str("room_jid", roomJID).Msg("Failed to backfill MUC MAM history")
				msg := fmt.Sprintf("MUC backfill failed for %s: %v", roomJID, err)
				if nc.fullSyncRequested {
					nc.reportSyncError(msg)
				} else {
					nc.recordSyncError(msg)
				}
			}
		}
	}(roomJID, start)
}

func (nc *MyNetworkClient) scheduleMUCHistoryFallback(roomJID string, start time.Time) {
	if roomJID == "" {
		return
	}
	history := stanza.History{}
	if start.IsZero() {
		start = time.Unix(0, 0)
	}
	history.Since = start
	nc.mucHistoryMu.Lock()
	if nc.mucHistoryRequests == nil {
		nc.mucHistoryRequests = map[string]stanza.History{}
	}
	nc.mucHistoryRequests[roomJID] = history
	nc.mucHistoryMu.Unlock()
	nc.syncStatsMu.Lock()
	nc.syncStats.mucHistoryFallbacks++
	nc.syncStatsMu.Unlock()
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("room_jid", roomJID).
			Time("since", history.Since).
			Bool("since_epoch", history.Since.Equal(time.Unix(0, 0))).
			Msg("Scheduled MUC history fallback on join")
	}
}

func (nc *MyNetworkClient) maybeFallbackToMUCHistory(roomJID string, start time.Time, fin *MAMFin) {
	if roomJID == "" || !start.IsZero() || fin == nil || !fin.Complete {
		return
	}
	count := -1
	first := ""
	if fin.Set != nil {
		if fin.Set.Count != nil {
			count = *fin.Set.Count
		}
		if fin.Set.First != nil {
			first = fin.Set.First.Content
		}
	}
	if count > 0 || first != "" {
		return
	}
	nc.mucHistoryMu.Lock()
	if nc.mucHistoryFallbackAttempts == nil {
		nc.mucHistoryFallbackAttempts = map[string]bool{}
	}
	if nc.mucHistoryFallbackAttempts[roomJID] {
		nc.mucHistoryMu.Unlock()
		return
	}
	nc.mucHistoryFallbackAttempts[roomJID] = true
	nc.mucHistoryMu.Unlock()

	nick := ""
	if nc.mucNick != nil {
		nick = nc.mucNick[roomJID]
	}
	password := ""
	if nc.mucPasswords != nil {
		password = nc.mucPasswords[roomJID]
	}
	if nick == "" {
		nick = nc.defaultMUCNick()
	}

	nc.log.Warn().
		Str("room_jid", roomJID).
		Bool("start_zero", start.IsZero()).
		Int("count", count).
		Str("first", first).
		Msg("MAM returned no results; rejoining with MUC history fallback")
	// schedule history before rejoin so joinMUC uses history fallback
	nc.scheduleMUCHistoryFallback(roomJID, start)
	nc.joinMUC(roomJID, nick, password)
}

func (nc *MyNetworkClient) handleBookmarkConferences(confs []BookmarkConference, source string) {
	if len(confs) == 0 {
		return
	}
	seen := make(map[string]bool, len(confs))
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().Str("source", source).Int("items", len(confs)).Msg("Processing bookmarks")
	}
	for _, conf := range confs {
		roomJID := strings.TrimSpace(conf.JID)
		if roomJID == "" {
			continue
		}
		if seen[roomJID] {
			continue
		}
		seen[roomJID] = true
		logJIDNormalization(nc.log, "bookmark_room", roomJID)
		if nc.log.GetLevel() <= zerolog.DebugLevel {
			nc.log.Debug().
				Str("source", source).
				Str("room_jid", roomJID).
				Str("autojoin", conf.Autojoin).
				Str("nick", strings.TrimSpace(conf.Nick)).
				Msg("Found bookmarked room")
		}
		alreadyBookmarked := false
		if nc.bookmarkedRooms != nil {
			alreadyBookmarked = nc.bookmarkedRooms[roomJID]
			nc.bookmarkedRooms[roomJID] = true
		}
		if alreadyBookmarked {
			if nc.log.GetLevel() <= zerolog.DebugLevel {
				nc.log.Debug().Str("source", source).Str("room_jid", roomJID).Msg("Skipping duplicate bookmarked room")
			}
			continue
		}
		name := groupRoomNameFromJID(context.Background(), nc.bridge, roomJID, conf.Name, nc.contactNameSuffix())

		portalID := MakePortalID(roomJID)
		if nc.log.GetLevel() <= zerolog.DebugLevel {
			nc.log.Debug().
				Str("raw_jid", roomJID).
				Str("portal_id", string(portalID)).
				Msg("Resolved portal ID for bookmark")
		}
		resync := &simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type:         bridgev2.RemoteEventChatResync,
				PortalKey:    networkid.PortalKey{ID: portalID},
				CreatePortal: true,
				Timestamp:    time.Now(),
			},
			ChatInfo: &bridgev2.ChatInfo{
				Name: &name,
				Type: ptr.Ptr(database.RoomTypeDefault),
			},
		}
		nc.log.Debug().Str("portal_id", string(portalID)).Msg("Queueing bookmarked groupchat resync")
		nc.bridge.QueueRemoteEvent(nc.login, resync)
		nc.recordMUCBookmarkPortal(string(portalID))

		nick := strings.TrimSpace(conf.Nick)
		if nick == "" {
			nick = nc.defaultMUCNick()
		}
		shouldJoin := conf.AutojoinEnabled()
		if !shouldJoin && nc.log.GetLevel() <= zerolog.DebugLevel {
			nc.log.Debug().Str("room_jid", roomJID).Str("source", source).Msg("Skipping bookmark join because autojoin is disabled")
		}
		if shouldJoin {
			start := time.Time{}
			if nc.fullSyncRequested {
				if nc.fullSyncUseStart {
					start = nc.fullSyncStart
				}
			} else {
				start = nc.historySyncStartForPortal(context.Background(), networkid.PortalKey{ID: portalID})
			}
			nc.scheduleMUCBackfill(roomJID, start)
			nc.joinMUC(roomJID, nick, conf.Password)
		}
	}
}

func (nc *MyNetworkClient) popMUCHistoryRequest(roomJID string) (stanza.History, bool) {
	nc.mucHistoryMu.Lock()
	defer nc.mucHistoryMu.Unlock()
	if nc.mucHistoryRequests == nil {
		return stanza.History{}, false
	}
	h, ok := nc.mucHistoryRequests[roomJID]
	if ok {
		delete(nc.mucHistoryRequests, roomJID)
	}
	return h, ok
}

func (nc *MyNetworkClient) resetSyncStats() {
	nc.syncStatsMu.Lock()
	nc.syncStats = syncStats{
		dmPortals:          map[string]bool{},
		mucBookmarkPortals: map[string]bool{},
		mucDiscoPortals:    map[string]bool{},
		lastErrors:         []string{},
	}
	nc.syncStatsMu.Unlock()
}

func (nc *MyNetworkClient) recordDMPortal(portalID string) {
	if portalID == "" {
		return
	}
	nc.syncStatsMu.Lock()
	if nc.syncStats.dmPortals == nil {
		nc.syncStats.dmPortals = map[string]bool{}
	}
	nc.syncStats.dmPortals[portalID] = true
	nc.syncStatsMu.Unlock()
}

func (nc *MyNetworkClient) recordMUCBookmarkPortal(portalID string) {
	if portalID == "" {
		return
	}
	nc.syncStatsMu.Lock()
	if nc.syncStats.mucBookmarkPortals == nil {
		nc.syncStats.mucBookmarkPortals = map[string]bool{}
	}
	nc.syncStats.mucBookmarkPortals[portalID] = true
	nc.syncStatsMu.Unlock()
}

func (nc *MyNetworkClient) recordMUCDiscoPortal(portalID string) {
	if portalID == "" {
		return
	}
	nc.syncStatsMu.Lock()
	if nc.syncStats.mucDiscoPortals == nil {
		nc.syncStats.mucDiscoPortals = map[string]bool{}
	}
	nc.syncStats.mucDiscoPortals[portalID] = true
	nc.syncStatsMu.Unlock()
}

func (nc *MyNetworkClient) recordMAMBackfillDM() {
	nc.syncStatsMu.Lock()
	nc.syncStats.mamDMBackfills++
	nc.syncStatsMu.Unlock()
}

func (nc *MyNetworkClient) recordMAMBackfillMUC() {
	nc.syncStatsMu.Lock()
	nc.syncStats.mamMUCBackfills++
	nc.syncStatsMu.Unlock()
}

func (nc *MyNetworkClient) recordSyncError(msg string) {
	const maxErrors = 10
	if msg == "" {
		return
	}
	nc.syncStatsMu.Lock()
	for _, existing := range nc.syncStats.lastErrors {
		if existing == msg {
			nc.syncStatsMu.Unlock()
			return
		}
	}
	if len(nc.syncStats.lastErrors) < maxErrors {
		nc.syncStats.lastErrors = append(nc.syncStats.lastErrors, msg)
	} else {
		nc.syncStats.errorsTruncated = true
	}
	nc.syncStatsMu.Unlock()
}

func (nc *MyNetworkClient) notifyManagement(msg string) {
	if nc.login == nil || nc.login.User == nil {
		return
	}
	roomID := nc.login.User.ManagementRoom
	if roomID == "" {
		return
	}
	sendNotice(nc.bridge.Bot, roomID, msg)
}

func (nc *MyNetworkClient) reportSyncError(msg string) {
	nc.recordSyncError(msg)
	nc.syncMu.Lock()
	fullSyncRequested := nc.fullSyncRequested
	nc.syncMu.Unlock()
	if fullSyncRequested {
		return
	}
	nc.notifyManagement(msg)
}

func (nc *MyNetworkClient) syncSummaryText(status string) string {
	nc.syncStatsMu.Lock()
	dmCount := len(nc.syncStats.dmPortals)
	mucBookmarkCount := len(nc.syncStats.mucBookmarkPortals)
	mucDiscoCount := len(nc.syncStats.mucDiscoPortals)
	mamDM := nc.syncStats.mamDMBackfills
	mamMUC := nc.syncStats.mamMUCBackfills
	mucHistoryFallbacks := nc.syncStats.mucHistoryFallbacks
	errors := append([]string{}, nc.syncStats.lastErrors...)
	errorsTruncated := nc.syncStats.errorsTruncated
	nc.syncStatsMu.Unlock()

	lines := []string{
		fmt.Sprintf("Sync %s", status),
		fmt.Sprintf("DMs: %d", dmCount),
		fmt.Sprintf("Group chats (bookmarks): %d", mucBookmarkCount),
		fmt.Sprintf("Group chats (discovery): %d", mucDiscoCount),
		fmt.Sprintf("MAM backfills started: dm=%d muc=%d", mamDM, mamMUC),
		fmt.Sprintf("MUC history fallbacks: %d", mucHistoryFallbacks),
	}
	if len(errors) > 0 {
		lines = append(lines, "Errors:")
		lines = append(lines, errors...)
		if errorsTruncated {
			lines = append(lines, "(more errors truncated)")
		}
	}
	return strings.Join(lines, "\n")
}

func jidDomain(jid string) string {
	bare := strings.Split(jid, "/")[0]
	parts := strings.SplitN(bare, "@", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return bare
}

func logJIDNormalization(log zerolog.Logger, label string, raw string) {
	if log.GetLevel() > zerolog.DebugLevel {
		return
	}
	log.Debug().
		Str("label", label).
		Str("raw", raw).
		Str("escaped", EscapeJID(raw)).
		Str("unescaped", UnescapeJID(EscapeJID(raw))).
		Str("domain", jidDomain(raw)).
		Msg("JID normalization")
}

func (nc *MyNetworkClient) recordDiscoFeatures(from string, features []stanza.Feature) {
	if from == "" {
		return
	}
	nc.discoMu.Lock()
	if nc.discoFeatures == nil {
		nc.discoFeatures = map[string]map[string]bool{}
	}
	set := map[string]bool{}
	for _, feature := range features {
		if feature.Var == "" {
			continue
		}
		set[feature.Var] = true
	}
	nc.discoFeatures[from] = set
	if nc.mucMAMDiscoRequested != nil {
		delete(nc.mucMAMDiscoRequested, from)
	}
	nc.discoMu.Unlock()
}

func (nc *MyNetworkClient) discoveredRoomLists() (bookmarks []string, disco []string, joined []string, services []string) {
	if nc.bookmarkedRooms != nil {
		for jid := range nc.bookmarkedRooms {
			bookmarks = append(bookmarks, jid)
		}
	}
	if nc.mucRooms != nil {
		for jid := range nc.mucRooms {
			joined = append(joined, jid)
		}
	}
	if nc.mucServices != nil {
		for service := range nc.mucServices {
			services = append(services, service)
		}
	}
	nc.syncStatsMu.Lock()
	for portalID := range nc.syncStats.mucDiscoPortals {
		disco = append(disco, portalID)
	}
	nc.syncStatsMu.Unlock()
	sort.Strings(bookmarks)
	sort.Strings(disco)
	sort.Strings(joined)
	sort.Strings(services)
	return bookmarks, disco, joined, services
}

func (nc *MyNetworkClient) probeMUCServices(serverDomain string) {
	if serverDomain == "" {
		return
	}
	candidates := []string{
		"groups." + serverDomain,
		"conference." + serverDomain,
		"muc." + serverDomain,
		"rooms." + serverDomain,
		"chat." + serverDomain,
		serverDomain,
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		infoIq, _ := stanza.NewIQ(stanza.Attrs{
			Type: stanza.IQTypeGet,
			To:   candidate,
			Id:   fmt.Sprintf("disco_info_probe_%d", time.Now().UnixNano()),
		})
		infoIq.Payload = &stanza.DiscoInfo{}
		if err := nc.sendXMPP(infoIq); err != nil {
			nc.log.Debug().Err(err).Str("to", candidate).Msg("Skipping disco info probe")
		} else if nc.log.GetLevel() <= zerolog.DebugLevel {
			nc.log.Debug().Str("to", candidate).Msg("Probing disco info")
		}
		itemsIq, _ := stanza.NewIQ(stanza.Attrs{
			Type: stanza.IQTypeGet,
			To:   candidate,
			Id:   fmt.Sprintf("disco_items_probe_%d", time.Now().UnixNano()),
		})
		itemsIq.Payload = &stanza.DiscoItems{}
		if err := nc.sendXMPP(itemsIq); err != nil {
			nc.log.Debug().Err(err).Str("to", candidate).Msg("Skipping disco items probe")
		} else if nc.log.GetLevel() <= zerolog.DebugLevel {
			nc.log.Debug().Str("to", candidate).Msg("Probing disco items")
		}
	}
}

func (nc *MyNetworkClient) hasDiscoFeature(jid string, feature string) (bool, bool) {
	if jid == "" || feature == "" {
		return false, false
	}
	nc.discoMu.Lock()
	set, ok := nc.discoFeatures[jid]
	nc.discoMu.Unlock()
	if !ok {
		return false, false
	}
	return set[feature], true
}

func (nc *MyNetworkClient) bestMAMNamespaceForJID(jid string) (string, bool) {
	if jid == "" {
		return "", false
	}
	nc.discoMu.Lock()
	set, ok := nc.discoFeatures[jid]
	nc.discoMu.Unlock()
	if !ok {
		return "", false
	}
	ns := bestMAMNamespace(set)
	if ns == "" {
		return "", true
	}
	return ns, true
}

func (nc *MyNetworkClient) mamNamespaceForServer() (string, bool) {
	return nc.bestMAMNamespaceForJID(nc.ownBareJID())
}

func (nc *MyNetworkClient) mamNamespaceForMUC(roomJID string) (string, bool) {
	if ns, known := nc.bestMAMNamespaceForJID(roomJID); known {
		return ns, true
	}
	service := jidDomain(roomJID)
	if ns, known := nc.bestMAMNamespaceForJID(service); known {
		return ns, true
	}
	return "", false
}

func (nc *MyNetworkClient) mamNamespaceForTarget(toJID, withJID string) string {
	if toJID != "" {
		if ns, known := nc.mamNamespaceForMUC(toJID); known && ns != "" {
			return ns
		}
	} else if withJID != "" || (toJID == "" && withJID == "") {
		if ns, known := nc.mamNamespaceForServer(); known && ns != "" {
			return ns
		}
	}
	return NSMAM2
}

func (nc *MyNetworkClient) mamSupportedForServer() (bool, bool) {
	ns, known := nc.mamNamespaceForServer()
	return ns != "", known
}

func (nc *MyNetworkClient) mamSupportedForMUC(roomJID string) (bool, bool) {
	ns, known := nc.mamNamespaceForMUC(roomJID)
	return ns != "", known
}

func (nc *MyNetworkClient) ensureMAMSupportForServer() bool {
	supported, known := nc.mamSupportedForServer()
	if !known || supported {
		return true
	}
	nc.mamSupportMu.Lock()
	if !nc.mamServerUnsupportedNotified {
		nc.mamServerUnsupportedNotified = true
		nc.mamSupportMu.Unlock()
		nc.reportSyncError("Server does not advertise MAM support; DM history backfill is disabled.")
		return false
	}
	nc.mamSupportMu.Unlock()
	return false
}

func (nc *MyNetworkClient) ensureMAMSupportForMUC(roomJID string) bool {
	supported, known := nc.mamSupportedForMUC(roomJID)
	if !known || supported {
		return true
	}
	service := jidDomain(roomJID)
	nc.mamSupportMu.Lock()
	if nc.mamMUCUnsupportedNotified == nil {
		nc.mamMUCUnsupportedNotified = map[string]bool{}
	}
	if !nc.fullSyncRequested {
		if nc.mamMUCUnsupportedPendingSync == nil {
			nc.mamMUCUnsupportedPendingSync = map[string]bool{}
		}
		nc.mamMUCUnsupportedPendingSync[service] = true
		nc.mamSupportMu.Unlock()
		return false
	}
	if !nc.mamMUCUnsupportedNotified[service] {
		nc.mamMUCUnsupportedNotified[service] = true
		if nc.mamMUCUnsupportedPendingSync != nil {
			delete(nc.mamMUCUnsupportedPendingSync, service)
		}
		nc.mamSupportMu.Unlock()
		nc.reportSyncError(fmt.Sprintf("MUC service %s does not advertise MAM support; group history backfill is disabled.", service))
		return false
	}
	nc.mamSupportMu.Unlock()
	return false
}

func (nc *MyNetworkClient) createDM(ctx context.Context, jid string) error {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return fmt.Errorf("jid is empty")
	}
	name := jid
	portalID := MakePortalID(jid)
	resync := &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    networkid.PortalKey{ID: portalID},
			CreatePortal: true,
			Timestamp:    time.Now(),
		},
		ChatInfo: &bridgev2.ChatInfo{
			Name: &name,
			Type: ptr.Ptr(database.RoomTypeDM),
		},
	}
	nc.bridge.QueueRemoteEvent(nc.login, resync)
	return nil
}

// Connect is called to establish the connection to XMPP.
func (nc *MyNetworkClient) Connect(ctx context.Context) {
	attempt, started := nc.tryStartConnect()
	jid := nc.connectionJID()
	if !started {
		nc.log.Info().
			Str("jid", jid).
			Int("attempt", attempt).
			Dur("retry_in", 0).
			Msg("Skipping overlapping XMPP connection attempt")
		return
	}
	nc.log.Info().
		Str("jid", jid).
		Int("attempt", attempt).
		Dur("retry_in", 0).
		Msg("Starting XMPP connection attempt")
	nc.sendBridgeState(status.StateConnecting, "Connecting to XMPP server", 0)
	// These maps describe current XMPP session state. Rebuild them on every
	// connection so stale joins and bookmark deduplication cannot suppress
	// reconciliation after reconnect.
	nc.mucRooms = map[string]bool{}
	nc.mucJoined = map[string]bool{}
	nc.mucOccupants = map[string]map[string]string{}
	nc.mucNick = map[string]string{}
	nc.mucPasswords = map[string]string{}
	if nc.mucPendingCreate == nil {
		nc.mucPendingCreate = map[string]pendingMUCCreation{}
	}
	nc.mucBackfillPending = map[string]time.Time{}
	if nc.mucServices == nil {
		nc.mucServices = map[string]bool{}
	}
	nc.mucMAMDiscoRequested = map[string]bool{}
	nc.mucServiceDiscoRequested = map[string]bool{}
	nc.mucHistoryRequests = map[string]stanza.History{}
	if nc.mamMUCUnsupportedNotified == nil {
		nc.mamMUCUnsupportedNotified = map[string]bool{}
	}
	if nc.rosterItems == nil {
		nc.rosterMu.Lock()
		nc.rosterItems = map[string]bool{}
		nc.rosterMu.Unlock()
	}
	// Rebuild bookmark deduplication state from this connection's server
	// responses. Keeping it across reconnects would hide newly added bookmarks.
	nc.bookmarkedRooms = map[string]bool{}
	if nc.callSessions == nil {
		nc.callSessions = map[string]*xmppCallSessionState{}
	}
	nc.connectMu.Lock()
	nc.connected = false
	nc.connectedCh = make(chan struct{})
	nc.connectMu.Unlock()

	meta, ok := nc.login.Metadata.(*LoginMetadata)
	if !ok || meta == nil {
		err := fmt.Errorf("LoginMetadata is missing or invalid")
		nc.releaseConnect()
		nc.log.Error().
			Err(err).
			Str("jid", jid).
			Int("attempt", attempt).
			Dur("retry_in", 0).
			Msg("XMPP connection attempt failed")
		return
	}

	config := xmpp.Config{
		TransportConfiguration: xmpp.TransportConfiguration{},
		Jid:                    meta.RemoteUserID,
		Credential:             xmpp.Password(meta.AccessToken),
		Insecure:               false,
		StreamManagementEnable: true,
		StreamManagementResume: true,
	}

	nc.xmppRouter = xmpp.NewRouter()
	nc.xmppRouter.HandleFunc("message", nc.handleXMPPMessage)
	nc.xmppRouter.HandleFunc("presence", nc.handleXMPPPresence)
	nc.xmppRouter.HandleFunc("iq", nc.handleXMPPIq)

	client, err := xmpp.NewClient(&config, nc.xmppRouter, func(err error) {
		nc.log.Err(err).Str("jid", config.Jid).Int("attempt", attempt).Msg("XMPP error")
	})
	if err != nil {
		nc.releaseConnect()
		nc.log.Error().
			Err(err).
			Str("jid", config.Jid).
			Int("attempt", attempt).
			Dur("retry_in", 0).
			Msg("XMPP connection attempt failed while creating client")
		nc.sendBridgeState(status.StateUnknownError, fmt.Sprintf("Failed to create XMPP client: %v", err), 60)
		return
	}

	nc.connectMu.Lock()
	nc.xmppClient = client
	nc.connectMu.Unlock()
	client.SetHandler(func(event xmpp.Event) error {
		if event.State.GetState() == xmpp.StateDisconnected {
			nc.log.Warn().Msg("XMPP client reported disconnected state")
			nc.handleDisconnect(client, nil)
		}
		return nil
	})
	nc.log.Info().
		Str("jid", config.Jid).
		Int("attempt", attempt).
		Dur("retry_in", 0).
		Bool("insecure", config.Insecure).
		Msg("XMPP connection attempt in progress")

	client.PostConnectHook = func() error {
		nc.postConnectMu.Lock()
		defer nc.postConnectMu.Unlock()
		nc.connectMu.Lock()
		connectAttempt := nc.reconnectAttempt
		if connectAttempt == 0 {
			connectAttempt = 1
		}
		nc.connectMu.Unlock()
		nc.log.Info().
			Str("jid", config.Jid).
			Int("attempt", connectAttempt).
			Dur("retry_in", 0).
			Msg("XMPP connection succeeded, running post connect hook")
		if client.Session == nil || client.Session.BindJid == "" {
			return fmt.Errorf("XMPP session is not ready in post connect hook")
		}
		nc.connectMu.Lock()
		if !nc.connected {
			nc.connected = true
			if nc.connectedCh != nil {
				close(nc.connectedCh)
				nc.connectedCh = nil
			}
		}
		nc.reconnectAttempt = 0
		nc.connectMu.Unlock()
		nc.sendBridgeState(status.StateConnected, "Connected to XMPP server", 0)
		nc.sessionEstablishedAt = time.Now().UTC()

		// 1. Request Roster
		iq, _ := stanza.NewIQ(stanza.Attrs{
			Type: stanza.IQTypeGet,
			Id:   fmt.Sprintf("roster_%d", time.Now().UnixNano()),
		})
		iq.Payload = &stanza.Roster{}
		if err := client.Send(iq); err != nil {
			nc.log.Err(err).Msg("Failed to send roster request")
		} else {
			nc.log.Debug().Msg("Sent roster request")
		}

		// 2. Request Server Disco Info
		bindJid := client.Session.BindJid
		serverDomain := ""
		parts := strings.SplitN(bindJid, "@", 2)
		if len(parts) == 2 {
			serverDomain = parts[1]
		} else {
			serverDomain = bindJid
		}
		serverDomain = strings.SplitN(serverDomain, "/", 2)[0]
		if serverDomain == "" {
			nc.log.Warn().Str("bind_jid", bindJid).Msg("Skipping disco: could not parse server domain")
			return nil
		}
		discoIq, _ := stanza.NewIQ(stanza.Attrs{
			Type: stanza.IQTypeGet,
			To:   serverDomain,
			Id:   fmt.Sprintf("disco_info_%d", time.Now().UnixNano()),
		})
		discoIq.Payload = &stanza.DiscoInfo{}
		if err := client.Send(discoIq); err != nil {
			nc.log.Err(err).Msg("Failed to send disco info request")
		} else {
			nc.log.Debug().Str("to", serverDomain).Msg("Sent disco info request")
		}
		nc.requestOwnDiscoInfo()

		discoItemsIq, _ := stanza.NewIQ(stanza.Attrs{
			Type: stanza.IQTypeGet,
			To:   serverDomain,
			Id:   fmt.Sprintf("disco_items_%d", time.Now().UnixNano()),
		})
		discoItemsIq.Payload = &stanza.DiscoItems{}
		if err := client.Send(discoItemsIq); err != nil {
			nc.log.Err(err).Msg("Failed to send disco items request")
		} else {
			nc.log.Debug().Str("to", serverDomain).Msg("Sent disco items request")
		}
		nc.probeMUCServices(serverDomain)

		// 3. Enable Message Carbons
		carbonsIq, _ := stanza.NewIQ(stanza.Attrs{
			Type: stanza.IQTypeSet,
			Id:   fmt.Sprintf("carbons_%d", time.Now().UnixNano()),
		})
		carbonsIq.Payload = &CarbonsEnable{}
		if err := client.Send(carbonsIq); err != nil {
			nc.log.Err(err).Msg("Failed to enable message carbons")
		} else {
			nc.log.Debug().Msg("Enabled message carbons")
		}

		// 4. Request Bookmarks for group chats
		if err := nc.requestBookmarks(); err != nil {
			nc.log.Err(err).Msg("Failed to send bookmarks request")
		} else {
			nc.log.Debug().Msg("Sent bookmarks request")
		}
		nc.triggerAutomaticDMCatchup()

		// Initial Presence is sent automatically by the library

		return nil
	}
	client.PostResumeHook = func() error {
		nc.postConnectMu.Lock()
		defer nc.postConnectMu.Unlock()
		nc.connectMu.Lock()
		connectAttempt := nc.reconnectAttempt
		if connectAttempt == 0 {
			connectAttempt = 1
		}
		nc.connectMu.Unlock()
		nc.log.Info().
			Str("jid", config.Jid).
			Int("attempt", connectAttempt).
			Dur("retry_in", 0).
			Msg("XMPP stream resumed successfully")
		if client.Session == nil || client.Session.BindJid == "" {
			return fmt.Errorf("XMPP session is not ready after resume")
		}
		nc.connectMu.Lock()
		if !nc.connected {
			nc.connected = true
			if nc.connectedCh != nil {
				close(nc.connectedCh)
				nc.connectedCh = nil
			}
		}
		nc.reconnectAttempt = 0
		nc.connectMu.Unlock()
		nc.sendBridgeState(status.StateConnected, "Reconnected to XMPP server", 0)
		nc.sessionEstablishedAt = time.Now().UTC()
		nc.triggerAutomaticDMCatchup()
		nc.triggerAutomaticMUCCatchup()
		return nil
	}

	err = client.Connect()
	if err != nil {
		nc.log.Error().
			Err(err).
			Str("jid", config.Jid).
			Int("attempt", attempt).
			Dur("retry_in", 0).
			Msg("XMPP connection attempt failed")
		nc.handleDisconnect(client, err)
		return
	}
	nc.startRuntimeHealth(client)
}

func (nc *MyNetworkClient) sendBridgeState(stateEvent status.BridgeStateEvent, message string, ttl int) {
	if nc.login == nil || nc.login.BridgeState == nil {
		return
	}
	nc.bridgeStateMu.Lock()
	if nc.lastBridgeState == stateEvent {
		nc.bridgeStateMu.Unlock()
		return
	}
	nc.lastBridgeState = stateEvent
	nc.bridgeStateMu.Unlock()
	state := status.BridgeState{StateEvent: stateEvent, TTL: ttl, Message: message}
	nc.login.BridgeState.Send(state)
}

func (nc *MyNetworkClient) sendXMPP(packet stanza.Packet) error {
	client, connected, _ := nc.connectionSnapshot()
	if client == nil {
		return fmt.Errorf("XMPP client not connected")
	}
	if client.Session == nil {
		return fmt.Errorf("XMPP session is not established")
	}
	if !connected {
		return fmt.Errorf("XMPP client is disconnecting")
	}
	return client.Send(packet)
}

func (nc *MyNetworkClient) isXMPPConnected() bool {
	client, connected, _ := nc.connectionSnapshot()
	return connected && client != nil && client.Session != nil
}

func (nc *MyNetworkClient) connectionSnapshot() (client *xmpp.Client, connected bool, reconnectAttempt int) {
	nc.connectMu.Lock()
	client = nc.xmppClient
	connected = nc.connected
	reconnectAttempt = nc.reconnectAttempt
	nc.connectMu.Unlock()
	return client, connected, reconnectAttempt
}

func (nc *MyNetworkClient) ConnectionStatus() loginConnectionStatus {
	client, connected, reconnectAttempt := nc.connectionSnapshot()
	status := loginConnectionStatus{
		Connected:        connected && client != nil && client.Session != nil,
		ReconnectAttempt: reconnectAttempt,
	}
	if client != nil && client.Session != nil {
		status.SessionEstablished = true
		status.BindJID = client.Session.BindJid
	}
	nc.postConnectMu.Lock()
	sessionEstablishedAt := nc.sessionEstablishedAt
	nc.postConnectMu.Unlock()
	if !sessionEstablishedAt.IsZero() {
		status.Uptime = time.Since(sessionEstablishedAt).Truncate(time.Second)
	}
	return status
}

func (nc *MyNetworkClient) PingXMPP(ctx context.Context) (time.Duration, error) {
	client, connected, _ := nc.connectionSnapshot()
	if client == nil {
		return 0, fmt.Errorf("XMPP client not connected")
	}
	if client.Session == nil {
		return 0, fmt.Errorf("XMPP session is not established")
	}
	if !connected {
		return 0, fmt.Errorf("XMPP client is disconnecting")
	}

	target := serverDomainFromJID(client.Session.BindJid)
	if target == "" {
		target = serverDomainFromJID(nc.ownBareJID())
	}
	if target == "" {
		return 0, fmt.Errorf("could not determine XMPP server domain")
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	iq, err := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		To:   target,
		Id:   fmt.Sprintf("ping_%d", time.Now().UnixNano()),
	})
	if err != nil {
		return 0, err
	}
	iq.Payload = &XMPPPing{}

	startedAt := time.Now()
	resultCh, err := client.SendIQ(ctx, iq)
	if err != nil {
		return 0, err
	}

	select {
	case resp, ok := <-resultCh:
		elapsed := time.Since(startedAt)
		if !ok {
			return elapsed, fmt.Errorf("XMPP ping result channel closed")
		}
		if resp.Type == stanza.IQTypeError {
			detail := formatIQError(resp.Error)
			if detail == "" {
				detail = "unknown error"
			}
			return elapsed, fmt.Errorf("XMPP ping returned error: %s", detail)
		}
		if resp.Type != stanza.IQTypeResult {
			return elapsed, fmt.Errorf("unexpected XMPP ping response type: %s", resp.Type)
		}
		return elapsed, nil
	case <-ctx.Done():
		return time.Since(startedAt), ctx.Err()
	}
}

func (nc *MyNetworkClient) RuntimeHealth(ctx context.Context) error {
	jid := nc.connectionJID()
	connectionStatus := nc.ConnectionStatus()
	if !connectionStatus.Connected {
		err := fmt.Errorf("XMPP connection is not established")
		nc.log.Warn().
			Err(err).
			Str("jid", jid).
			Int("attempt", connectionStatus.ReconnectAttempt).
			Dur("retry_in", 0).
			Msg("XMPP runtime health check failed")
		return err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rtt, err := nc.PingXMPP(checkCtx)
	if err != nil {
		nc.log.Warn().
			Err(err).
			Str("jid", jid).
			Int("attempt", connectionStatus.ReconnectAttempt).
			Dur("retry_in", 0).
			Msg("XMPP runtime health check failed")
		return fmt.Errorf("XMPP ping failed: %w", err)
	}
	nc.log.Debug().
		Str("jid", jid).
		Int("attempt", connectionStatus.ReconnectAttempt).
		Dur("retry_in", 0).
		Dur("rtt", rtt).
		Msg("XMPP runtime health check succeeded")
	return nil
}

func (nc *MyNetworkClient) startRuntimeHealth(client *xmpp.Client) {
	go func() {
		check := func() bool {
			nc.connectMu.Lock()
			current := nc.xmppClient == client && nc.connected
			nc.connectMu.Unlock()
			if !current {
				return false
			}

			if err := nc.RuntimeHealth(context.Background()); err != nil {
				nc.connectMu.Lock()
				current = nc.xmppClient == client && nc.connected
				nc.connectMu.Unlock()
				if current {
					nc.log.Error().Err(err).Msg("XMPP health check failed, forcing reconnect")
					_ = client.Disconnect()
				}
				return false
			}
			return true
		}

		if !check() {
			return
		}
		ticker := time.NewTicker(xmppHealthCheckInterval)
		defer ticker.Stop()
		for range ticker.C {
			if !check() {
				return
			}
		}
	}()
}

func serverDomainFromJID(jid string) string {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return ""
	}
	jid = strings.SplitN(jid, "/", 2)[0]
	if before, after, ok := strings.Cut(jid, "@"); ok {
		_ = before
		return after
	}
	return jid
}

func (nc *MyNetworkClient) handleDisconnect(client *xmpp.Client, connectErr error) {
	nc.connectMu.Lock()
	if client != nil && nc.xmppClient != client {
		nc.connectMu.Unlock()
		return
	}
	if nc.stopReconnect == nil {
		nc.connectStarted = false
		nc.connectMu.Unlock()
		return
	}
	stopReconnect := nc.stopReconnect
	canResume := client != nil && xmpp.IsStreamResumable(client) && client.Session != nil && client.Session.SMState.Id != ""
	nc.connected = false
	nc.connectedCh = make(chan struct{})
	nc.connectStarted = false
	nc.reconnectAttempt++
	attempt := nc.reconnectAttempt
	nc.connectMu.Unlock()

	nc.postConnectMu.Lock()
	nc.sessionEstablishedAt = time.Time{}
	nc.postConnectMu.Unlock()

	select {
	case <-stopReconnect:
		nc.log.Info().Msg("XMPP reconnect loop stopped")
		return
	default:
	}

	backoff := xmppReconnectBackoff(attempt)
	nc.sendBridgeState(status.StateTransientDisconnect, fmt.Sprintf("Disconnected from XMPP server, retrying in %s", backoff.Round(time.Second)), int((backoff*2)/time.Second))
	jid := nc.connectionJID()
	resumeMode := "fresh reconnect"
	if canResume {
		resumeMode = "stream resume"
	}
	if connectErr != nil {
		nc.log.Warn().
			Err(connectErr).
			Str("jid", jid).
			Int("attempt", attempt).
			Dur("retry_in", backoff).
			Str("mode", resumeMode).
			Msg("XMPP connection lost, scheduling reconnect")
	} else {
		nc.log.Warn().
			Str("jid", jid).
			Int("attempt", attempt).
			Dur("retry_in", backoff).
			Str("mode", resumeMode).
			Msg("XMPP connection ended, scheduling reconnect")
	}

	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-stopReconnect:
		nc.log.Info().Msg("XMPP reconnect cancelled")
		return
	case <-timer.C:
	}

	nc.connectMu.Lock()
	select {
	case <-stopReconnect:
		nc.connectMu.Unlock()
		nc.log.Info().Msg("XMPP reconnect aborted after stop signal")
		return
	default:
	}
	if nc.connectStarted {
		nc.connectMu.Unlock()
		return
	}

	if canResume {
		nc.connectStarted = true
		nc.connectMu.Unlock()
		nc.log.Info().
			Str("jid", jid).
			Int("attempt", attempt).
			Dur("retry_in", 0).
			Str("mode", "stream resume").
			Msg("Launching XMPP reconnect")
		go func(resumeClient *xmpp.Client) {
			if err := resumeClient.Resume(); err != nil {
				nc.log.Warn().
					Err(err).
					Str("jid", jid).
					Int("attempt", attempt).
					Dur("retry_in", 0).
					Msg("XMPP stream resume failed, falling back to fresh reconnect")
				nc.connectMu.Lock()
				if nc.xmppClient == resumeClient {
					nc.xmppClient = nil
				}
				nc.connectStarted = false
				nc.connectMu.Unlock()
				nc.handleDisconnect(nil, err)
				return
			}
			nc.startRuntimeHealth(resumeClient)
		}(client)
		return
	}
	nc.connectMu.Unlock()

	nc.connectMu.Lock()
	if nc.xmppClient == client {
		nc.xmppClient = nil
	}
	nc.connectMu.Unlock()
	nc.log.Info().
		Str("jid", jid).
		Int("attempt", attempt).
		Dur("retry_in", 0).
		Str("mode", "fresh reconnect").
		Msg("Launching XMPP reconnect")
	go nc.Connect(context.Background())
}

func (nc *MyNetworkClient) handleXMPPPresence(s xmpp.Sender, p stanza.Packet) {
	pres, ok := p.(stanza.Presence)
	if !ok {
		nc.log.Debug().Type("packet_type", p).Msg("Received non-presence packet on presence handler")
		return
	}
	nc.log.Info().
		Str("from", pres.From).
		Str("type", string(pres.Type)).
		Msg("Received XMPP presence")
	nc.log.Debug().Interface("presence", pres).Msg("Presence details")
	if pres.Type == stanza.PresenceTypeError {
		nc.log.Warn().
			Str("from", pres.From).
			Int("code", pres.Error.Code).
			Str("reason", pres.Error.Reason).
			Str("text", pres.Error.Text).
			Msg("Presence error received")
	}

	bareFrom := strings.Split(pres.From, "/")[0]
	// Treat group-DM style JIDs as MUC rooms if they have commas
	if bareFrom != "" {
		isMUC := nc.mucRooms != nil && nc.mucRooms[bareFrom]
		if !isMUC && strings.Contains(bareFrom, ",") && strings.HasSuffix(bareFrom, "@cheogram.com") {
			isMUC = true
		}
		if isMUC {
			resource := ""
			if parts := strings.SplitN(pres.From, "/", 2); len(parts) == 2 {
				resource = parts[1]
			}
			var mucUser MUCUserInvite
			hasMUCUser := pres.Get(&mucUser)
			isSelfPresence := nc.isSelfMUCPresence(bareFrom, resource, func() *MUCUserInvite {
				if hasMUCUser {
					return &mucUser
				}
				return nil
			}())
			if pres.Type != stanza.PresenceTypeUnavailable && isSelfPresence {
				if nc.mucJoined == nil {
					nc.mucJoined = map[string]bool{}
				}
				nc.mucJoined[bareFrom] = true
				nc.maybeStartPendingMUCBackfill(bareFrom)
				if pending, ok := nc.popPendingMUCCreation(bareFrom); ok {
					go func(roomJID string, pending pendingMUCCreation) {
						if err := nc.configureMUC(context.Background(), roomJID, pending.name); err != nil {
							nc.log.Err(err).Str("room_jid", roomJID).Msg("Failed to configure newly created MUC")
							return
						}
						for _, participant := range pending.participants {
							if err := nc.inviteToMUC(roomJID, participant, "JMPChat group chat"); err != nil {
								nc.log.Err(err).Str("room_jid", roomJID).Str("jid", participant).Msg("Failed to invite participant")
							}
						}
					}(bareFrom, pending)
				}
			} else if pres.Type == stanza.PresenceTypeUnavailable && isSelfPresence && nc.mucJoined != nil {
				delete(nc.mucJoined, bareFrom)
			}
			if nc.mucOccupants == nil {
				nc.mucOccupants = map[string]map[string]string{}
			}
			occupants := nc.mucOccupants[bareFrom]
			if occupants == nil {
				occupants = map[string]string{}
				nc.mucOccupants[bareFrom] = occupants
			}
			if pres.Type == stanza.PresenceTypeUnavailable {
				delete(occupants, pres.From)
			} else if pres.From != "" && strings.Contains(pres.From, "/") {
				if isSelfPresence {
					if nc.log.GetLevel() <= zerolog.DebugLevel {
						nc.log.Debug().
							Str("room_jid", bareFrom).
							Str("nick", resource).
							Msg("Skipping self occupant")
					}
				} else {
					occupants[pres.From] = nc.resolveMUCPresenceMemberJID(bareFrom, pres, resource, func() *MUCUserInvite {
						if hasMUCUser {
							return &mucUser
						}
						return nil
					}())
				}
			}

			memberMap := bridgev2.ChatMemberMap{}
			for _, memberJID := range occupants {
				if memberJID == "" || memberJID == nc.ownBareJID() {
					continue
				}
				memberMap[MakeUserID(memberJID)] = bridgev2.ChatMember{
					EventSender: bridgev2.EventSender{Sender: MakeUserID(memberJID)},
					Membership:  event.MembershipJoin,
				}
			}
			members := &bridgev2.ChatMemberList{
				IsFull:                     true,
				CheckAllLogins:             true,
				ExcludeChangesFromTimeline: true,
				TotalMemberCount:           len(memberMap),
				MemberMap:                  memberMap,
			}
			chatInfo := &bridgev2.ChatInfo{Members: members}
			change := &simplevent.ChatInfoChange{
				EventMeta: simplevent.EventMeta{
					Type:      bridgev2.RemoteEventChatInfoChange,
					PortalKey: networkid.PortalKey{ID: MakePortalID(bareFrom)},
					Timestamp: time.Now(),
				},
				ChatInfoChange: &bridgev2.ChatInfoChange{ChatInfo: chatInfo},
			}
			nc.bridge.QueueRemoteEvent(nc.login, change)
		}
	}
}

func (nc *MyNetworkClient) handleXMPPIq(s xmpp.Sender, p stanza.Packet) {
	iq, ok := p.(*stanza.IQ)
	if !ok {
		nc.log.Debug().Type("packet_type", p).Msg("Received non-IQ packet on IQ handler")
		return
	}
	nc.log.Info().
		Str("from", iq.From).
		Str("type", string(iq.Type)).
		Str("id", iq.Id).
		Msg("Received XMPP IQ")
	nc.log.Debug().Interface("payload", iq.Payload).Msg("IQ payload details")

	if iq.Type == stanza.IQTypeResult {
		switch payload := iq.Payload.(type) {
		case *stanza.RosterItems:
			if !nc.isXMPPConnected() {
				nc.log.Debug().Msg("Skipping roster result follow-up while XMPP is disconnected")
				return
			}
			nc.log.Info().Int("items", len(payload.Items)).Msg("Received roster items")
			for _, item := range payload.Items {
				bareJID := strings.Split(item.Jid, "/")[0]
				if bareJID == "" {
					continue
				}
				logJIDNormalization(nc.log, "roster_item", bareJID)
				if nc.log.GetLevel() <= zerolog.DebugLevel {
					nc.log.Debug().
						Str("jid", bareJID).
						Str("subscription", string(item.Subscription)).
						Msg("Roster item")
				}
				discoIq, _ := stanza.NewIQ(stanza.Attrs{
					Type: stanza.IQTypeGet,
					To:   bareJID,
					Id:   fmt.Sprintf("disco_info_roster_%d", time.Now().UnixNano()),
				})
				discoIq.Payload = &stanza.DiscoInfo{}
				if err := nc.sendXMPP(discoIq); err != nil {
					nc.log.Err(err).Str("to", bareJID).Msg("Failed to send disco info for roster item")
				}
				nc.rosterMu.Lock()
				if nc.rosterItems != nil {
					nc.rosterItems[bareJID] = true
				}
				nc.rosterMu.Unlock()
				name := dmRoomName(context.Background(), nc.bridge, bareJID, item.Name, nc.contactNameSuffix())
				portalID := MakePortalID(bareJID)
				if nc.log.GetLevel() <= zerolog.DebugLevel {
					nc.log.Debug().
						Str("raw_jid", bareJID).
						Str("portal_id", string(portalID)).
						Msg("Resolved portal ID for roster item")
				}
				resync := &simplevent.ChatResync{
					EventMeta: simplevent.EventMeta{
						Type:         bridgev2.RemoteEventChatResync,
						PortalKey:    networkid.PortalKey{ID: portalID},
						CreatePortal: true,
						Timestamp:    time.Now(),
					},
					ChatInfo: &bridgev2.ChatInfo{
						Name: &name,
						Type: ptr.Ptr(database.RoomTypeDM),
					},
				}
				nc.log.Debug().Str("portal_id", string(portalID)).Msg("Queueing roster chat resync")
				nc.bridge.QueueRemoteEvent(nc.login, resync)
				nc.recordDMPortal(string(portalID))
				if nc.fullSyncRequested {
					start := time.Time{}
					if nc.fullSyncUseStart {
						start = nc.fullSyncStart
					}
					nc.scheduleDMBackfill(bareJID, start)
				}
			}
			if nc.fullSyncRosterPending {
				nc.fullSyncRosterPending = false
				nc.maybeClearFullSync()
			}
			nc.maybeStartPendingDMBackfills()
		case *stanza.Roster:
			nc.log.Debug().Msg("Received roster result without items")
		case *BookmarkStorage:
			nc.log.Info().Int("items", len(payload.Conferences)).Str("from", string(iq.From)).Msg("Received bookmarks")
			if !nc.isXMPPConnected() {
				nc.log.Debug().Msg("Skipping bookmark follow-up while XMPP is disconnected")
				return
			}
			nc.handleBookmarkConferences(payload.Conferences, "storage")
			if nc.fullSyncBookmarksPending {
				nc.fullSyncBookmarksPending = false
				nc.maybeClearFullSync()
			}
		case *PrivateBookmarkQuery:
			if payload.Storage == nil {
				nc.log.Info().Msg("Received empty bookmark storage")
				return
			}
			nc.log.Info().Int("items", len(payload.Storage.Conferences)).Str("from", string(iq.From)).Msg("Received bookmarks")
			if !nc.isXMPPConnected() {
				nc.log.Debug().Msg("Skipping private bookmark follow-up while XMPP is disconnected")
				return
			}
			nc.handleBookmarkConferences(payload.Storage.Conferences, "private")
			if nc.fullSyncBookmarksPending {
				nc.fullSyncBookmarksPending = false
				nc.maybeClearFullSync()
			}
		case *stanza.PubSubGeneric:
			if payload.Items == nil || payload.Items.Node != NSBookmarks2 {
				return
			}
			if nc.log.GetLevel() <= zerolog.DebugLevel {
				nc.log.Debug().
					Str("from", string(iq.From)).
					Str("id", iq.Id).
					Str("type", string(iq.Type)).
					Str("node", payload.Items.Node).
					Int("items", len(payload.Items.List)).
					Msg("Received bookmarks2 pubsub items")
			}
			if len(payload.Items.List) == 0 {
				nc.log.Info().Msg("Received empty bookmarks2 list")
				return
			}
			confs := make([]BookmarkConference, 0, len(payload.Items.List))
			for _, item := range payload.Items.List {
				if item.Any == nil {
					continue
				}
				if item.Any.XMLName.Space == NSBookmarks2 && item.Any.XMLName.Local == "conference" {
					data, err := xml.Marshal(item.Any)
					if err != nil {
						nc.log.Err(err).Msg("Failed to marshal bookmarks2 item")
						continue
					}
					if nc.log.GetLevel() <= zerolog.DebugLevel {
						nc.log.Debug().
							Str("item_id", item.Id).
							Str("payload", string(data)).
							Msg("Parsed bookmarks2 conference item")
					}
					var conf BookmarkConference2
					if err := xml.Unmarshal(data, &conf); err != nil {
						nc.log.Err(err).Msg("Failed to parse bookmarks2 item")
						continue
					}
					confs = append(confs, BookmarkConference{
						JID:      conf.JID,
						Name:     conf.Name,
						Autojoin: conf.Autojoin,
						Nick:     conf.Nick,
						Password: conf.Password,
					})
					continue
				}
				if item.Any.XMLName.Space == "storage:bookmarks" && item.Any.XMLName.Local == "storage" {
					data, err := xml.Marshal(item.Any)
					if err != nil {
						nc.log.Err(err).Msg("Failed to marshal bookmarks conversion item")
						continue
					}
					if nc.log.GetLevel() <= zerolog.DebugLevel {
						nc.log.Debug().
							Str("item_id", item.Id).
							Str("payload", string(data)).
							Msg("Parsed bookmarks conversion item")
					}
					var storage BookmarkStorage
					if err := xml.Unmarshal(data, &storage); err != nil {
						nc.log.Err(err).Msg("Failed to parse bookmarks conversion item")
						continue
					}
					confs = append(confs, storage.Conferences...)
				}
			}
			nc.log.Info().Int("items", len(confs)).Msg("Received bookmarks2 list")
			if !nc.isXMPPConnected() {
				nc.log.Debug().Msg("Skipping bookmarks2 follow-up while XMPP is disconnected")
				return
			}
			nc.handleBookmarkConferences(confs, "bookmarks2")
		case *stanza.DiscoItems:
			if !nc.isXMPPConnected() {
				nc.log.Debug().Str("from", string(iq.From)).Msg("Skipping disco items follow-up while XMPP is disconnected")
				return
			}
			if nc.log.GetLevel() <= zerolog.DebugLevel {
				items := make([]string, 0, len(payload.Items))
				for _, item := range payload.Items {
					if item.JID != "" {
						items = append(items, item.JID)
					}
				}
				nc.log.Debug().
					Str("from", string(iq.From)).
					Str("id", iq.Id).
					Int("items", len(payload.Items)).
					Strs("item_jids", items).
					Msg("Received disco items")
			}
			if iq.From != "" && nc.mucServices != nil && nc.mucServices[iq.From] {
				for _, item := range payload.Items {
					roomJID := strings.TrimSpace(item.JID)
					if roomJID == "" {
						continue
					}
					logJIDNormalization(nc.log, "disco_room", roomJID)
					if nc.bookmarkedRooms != nil && nc.bookmarkedRooms[roomJID] {
						continue
					}
					portalID := MakePortalID(roomJID)
					if nc.log.GetLevel() <= zerolog.DebugLevel {
						nc.log.Debug().
							Str("raw_jid", roomJID).
							Str("portal_id", string(portalID)).
							Msg("Resolved portal ID for disco room")
					}
					resync := &simplevent.ChatResync{
						EventMeta: simplevent.EventMeta{
							Type:         bridgev2.RemoteEventChatResync,
							PortalKey:    networkid.PortalKey{ID: portalID},
							CreatePortal: true,
							Timestamp:    time.Now(),
						},
						ChatInfo: &bridgev2.ChatInfo{
							Name: ptr.Ptr(groupRoomNameFromJID(context.Background(), nc.bridge, roomJID, "", nc.contactNameSuffix())),
							Type: ptr.Ptr(database.RoomTypeDefault),
						},
					}
					nc.log.Debug().Str("portal_id", string(portalID)).Msg("Queueing discovered groupchat resync")
					nc.bridge.QueueRemoteEvent(nc.login, resync)
					nc.recordMUCDiscoPortal(string(portalID))
				}
				return
			}
			if len(payload.Items) == 0 {
				nc.log.Debug().Msg("Received disco items result with no items")
				return
			}
			for _, item := range payload.Items {
				if item.JID == "" {
					continue
				}
				discoIq, _ := stanza.NewIQ(stanza.Attrs{
					Type: stanza.IQTypeGet,
					To:   item.JID,
					Id:   fmt.Sprintf("disco_info_%d", time.Now().UnixNano()),
				})
				discoIq.Payload = &stanza.DiscoInfo{}
				if err := nc.sendXMPP(discoIq); err != nil {
					nc.log.Err(err).Str("to", item.JID).Msg("Failed to send disco info request")
				}
			}
		case *stanza.DiscoInfo:
			nc.recordDiscoFeatures(iq.From, payload.Features)
			if iq.From == nc.ownBareJID() {
				nc.maybeStartPendingDMBackfills()
			}
			nc.maybeStartPendingMUCBackfill(iq.From)
			if iq.From != "" {
				nc.mucBackfillMu.Lock()
				pendingRooms := make([]string, 0, len(nc.mucBackfillPending))
				for roomJID := range nc.mucBackfillPending {
					if jidDomain(roomJID) == iq.From {
						pendingRooms = append(pendingRooms, roomJID)
					}
				}
				nc.mucBackfillMu.Unlock()
				for _, roomJID := range pendingRooms {
					nc.maybeStartPendingMUCBackfill(roomJID)
				}
			}
			isMUC := isMUCService(payload)
			if nc.log.GetLevel() <= zerolog.DebugLevel {
				features := make([]string, 0, len(payload.Features))
				for _, feature := range payload.Features {
					if feature.Var != "" {
						features = append(features, feature.Var)
					}
				}
				identities := make([]string, 0, len(payload.Identity))
				for _, identity := range payload.Identity {
					identities = append(identities, fmt.Sprintf("%s/%s/%s", identity.Category, identity.Type, identity.Name))
				}
				nc.log.Debug().
					Str("from", string(iq.From)).
					Bool("is_muc_service", isMUC).
					Int("features", len(features)).
					Strs("feature_vars", features).
					Int("identities", len(identities)).
					Strs("identity_list", identities).
					Msg("Received disco info")
			}
			if isMUC && iq.From != "" {
				if nc.mucServices != nil {
					nc.mucServices[iq.From] = true
				}
				if nc.fullSyncRequested && nc.mucServiceDiscoRequested != nil && !nc.mucServiceDiscoRequested[iq.From] {
					nc.mucServiceDiscoRequested[iq.From] = true
					discoIq, _ := stanza.NewIQ(stanza.Attrs{
						Type: stanza.IQTypeGet,
						To:   iq.From,
						Id:   fmt.Sprintf("disco_items_muc_%d", time.Now().UnixNano()),
					})
					discoIq.Payload = &stanza.DiscoItems{}
					if err := nc.sendXMPP(discoIq); err != nil {
						nc.log.Err(err).Str("to", iq.From).Msg("Failed to send MUC disco items request")
						nc.reportSyncError(fmt.Sprintf("Failed to list rooms for MUC service %s: %v", iq.From, err))
					}
				}
			}
			for _, feature := range payload.Features {
				if feature.Var == NSHTTPUpload {
					if iq.From != "" && nc.httpUploadServiceJID == "" {
						nc.httpUploadServiceJID = iq.From
						nc.log.Info().Str("service", iq.From).Msg("Discovered HTTP upload service")
					}
					break
				}
			}
		}
	}
	if iq.Type == stanza.IQTypeError {
		if strings.HasPrefix(iq.Id, "disco_items_muc_") {
			nc.reportSyncError(fmt.Sprintf("MUC room listing failed for %s", iq.From))
		}
	}
}

func (nc *MyNetworkClient) handleXMPPMessage(s xmpp.Sender, p stanza.Packet) {
	msg, ok := p.(stanza.Message)
	if !ok {
		nc.log.Debug().Type("packet_type", p).Msg("Received non-message packet on message handler")
		return
	}
	var mam MAMResult
	if msg.Get(&mam) && mam.Forwarded != nil && mam.Forwarded.Message != nil {
		timestamp := time.Now()
		if mam.Forwarded.Delay != nil && mam.Forwarded.Delay.Stamp != "" {
			if parsed, err := time.Parse(time.RFC3339, mam.Forwarded.Delay.Stamp); err == nil {
				timestamp = parsed
			} else if parsed, err := time.Parse(time.RFC3339Nano, mam.Forwarded.Delay.Stamp); err == nil {
				timestamp = parsed
			}
		}
		fwd := *mam.Forwarded.Message
		if !isValidMAMForwardedJID(fwd.From) {
			nc.log.Warn().Str("from", fwd.From).Str("mam_id", mam.ID).Msg("Dropping MAM result with invalid forwarded from JID")
			return
		}
		idOverride := mam.ID
		if fwd.Id != "" {
			idOverride = fwd.Id
		}
		isFromMe := false
		if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil && meta.RemoteUserID != "" {
			bareFrom := strings.Split(fwd.From, "/")[0]
			if bareFrom == meta.RemoteUserID {
				isFromMe = true
			}
		}
		if mam.QueryID != "" && nc.storeMAMQueryResult(mam.QueryID, collectedMAMMessage{
			Message:    fwd,
			Timestamp:  timestamp,
			IDOverride: idOverride,
			IsFromMe:   isFromMe,
		}) {
			return
		}
		nc.handleIncomingXMPPMessage(fwd, timestamp, idOverride, isFromMe, true)
		return
	}

	var carbonSent XMPCarbonSent
	if msg.Get(&carbonSent) && carbonSent.Forwarded != nil {
		if forwarded, ok := carbonSent.Forwarded.Stanza.(stanza.Message); ok {
			nc.handleIncomingXMPPMessage(forwarded, xmppMessageTimestamp(forwarded, time.Now()), "", true, false)
			return
		}
	}
	var carbonReceived XMPCarbonReceived
	if msg.Get(&carbonReceived) && carbonReceived.Forwarded != nil {
		if forwarded, ok := carbonReceived.Forwarded.Stanza.(stanza.Message); ok {
			nc.handleIncomingXMPPMessage(forwarded, xmppMessageTimestamp(forwarded, time.Now()), "", false, false)
			return
		}
	}

	// XMPP JIDs look like user@domain.com/resource
	fromJidStr := msg.From
	fromParts := strings.Split(fromJidStr, "/")
	bareFromJid := fromParts[0]

	var mucInvite MUCUserInvite
	if msg.Get(&mucInvite) && mucInvite.Invite != nil {
		roomJID := strings.TrimSpace(bareFromJid)
		if roomJID == "" {
			roomJID = strings.TrimSpace(mucInvite.Invite.From)
		}
		if roomJID != "" {
			portalID := MakePortalID(roomJID)
			resync := &simplevent.ChatResync{
				EventMeta: simplevent.EventMeta{
					Type:         bridgev2.RemoteEventChatResync,
					PortalKey:    networkid.PortalKey{ID: portalID},
					CreatePortal: true,
					Timestamp:    time.Now(),
				},
				ChatInfo: &bridgev2.ChatInfo{
					Name: ptr.Ptr(groupRoomNameFromJID(context.Background(), nc.bridge, roomJID, "", nc.contactNameSuffix())),
					Type: ptr.Ptr(database.RoomTypeDefault),
				},
			}
			nc.log.Info().
				Str("room_jid", roomJID).
				Str("from", mucInvite.Invite.From).
				Str("reason", mucInvite.Invite.Reason).
				Msg("Received MUC invite")
			nc.bridge.QueueRemoteEvent(nc.login, resync)
			portalKey := networkid.PortalKey{ID: MakePortalID(roomJID)}
			nc.scheduleMUCBackfill(roomJID, nc.historySyncStartForPortal(context.Background(), portalKey))
			nc.joinMUC(roomJID, "", mucInvite.Password)
			if stanza.StringValue(msg.Body) == "" {
				return
			}
		}
	}

	var directInvite MUCDirectInvite
	if msg.Get(&directInvite) && directInvite.JID != "" {
		roomJID := strings.TrimSpace(directInvite.JID)
		if roomJID != "" {
			portalID := MakePortalID(roomJID)
			resync := &simplevent.ChatResync{
				EventMeta: simplevent.EventMeta{
					Type:         bridgev2.RemoteEventChatResync,
					PortalKey:    networkid.PortalKey{ID: portalID},
					CreatePortal: true,
					Timestamp:    time.Now(),
				},
				ChatInfo: &bridgev2.ChatInfo{
					Name: ptr.Ptr(groupRoomNameFromJID(context.Background(), nc.bridge, roomJID, "", nc.contactNameSuffix())),
					Type: ptr.Ptr(database.RoomTypeDefault),
				},
			}
			nc.log.Info().
				Str("room_jid", roomJID).
				Str("from", directInvite.From).
				Str("reason", directInvite.Reason).
				Msg("Received direct MUC invite")
			nc.bridge.QueueRemoteEvent(nc.login, resync)
			portalKey := networkid.PortalKey{ID: MakePortalID(roomJID)}
			nc.scheduleMUCBackfill(roomJID, nc.historySyncStartForPortal(context.Background(), portalKey))
			nc.joinMUC(roomJID, "", directInvite.Password)
			if stanza.StringValue(msg.Body) == "" {
				return
			}
		}
	}

	nc.log.Info().
		Str("from", fromJidStr).
		Str("type", string(msg.Type)).
		Str("body", stanza.StringValue(msg.Body)).
		Msg("Received XMPP message")
	nc.log.Debug().Interface("message", msg).Msg("Message details")

	nc.handleIncomingXMPPMessage(msg, time.Now(), "", false, false)
}

func isValidMAMForwardedJID(jid string) bool {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return true
	}
	_, err := stanza.NewJid(jid)
	return err == nil
}

func xmppMessageTimestamp(msg stanza.Message, fallback time.Time) time.Time {
	stamp := ""
	var delay XMPPDelay
	if msg.Get(&delay) {
		stamp = delay.Stamp
	} else {
		var delayOld XMPPDelayOld
		if msg.Get(&delayOld) {
			stamp = delayOld.InnerXML
		}
	}
	stamp = strings.TrimSpace(stamp)
	if stamp == "" {
		return fallback
	}
	for _, layout := range []string{time.RFC3339Nano, "20060102T15:04:05"} {
		if parsed, err := time.Parse(layout, stamp); err == nil {
			return parsed
		}
	}
	return fallback
}

func (nc *MyNetworkClient) handleIncomingXMPPMessage(msg stanza.Message, timestamp time.Time, idOverride string, isFromMe bool, fromMAM bool) {
	// XMPP JIDs look like user@domain.com/resource
	fromJidStr := msg.From
	fromParts := strings.Split(fromJidStr, "/")
	bareFromJid := fromParts[0]
	if isFromMe {
		if msg.To != "" {
			bareFromJid = strings.Split(msg.To, "/")[0]
		}
	}
	if bareFromJid == "" {
		return
	}
	logJIDNormalization(nc.log, "incoming_from", bareFromJid)
	nc.respondToIncomingXMPPReceipt(msg, isFromMe, fromMAM)

	isMUC := msg.Type == stanza.MessageTypeGroupchat
	// Always treat multi-recipient numbers on Cheogram as MUCs even if typed as chat
	if strings.Contains(bareFromJid, ",") && strings.HasSuffix(bareFromJid, "@cheogram.com") {
		isMUC = true
	}
	var delayStamp string
	var delay XMPPDelay
	if msg.Get(&delay) {
		delayStamp = delay.Stamp
	} else {
		var delayOld XMPPDelayOld
		if msg.Get(&delayOld) {
			delayStamp = delayOld.InnerXML
		}
	}
	attachment := extractXMPPAttachment(msg)
	replyTargetID := extractXMPPReplyTarget(msg)
	if replyTargetID != "" {
		msg.Body = stanza.StringPtr(trimBlankLines(strings.TrimSpace(stripXMPPFallbackFromMessage(msg, NSReply))))
	}
	if nc.handleIncomingXMPPCall(msg, timestamp, bareFromJid, isMUC, isFromMe) {
		return
	}

	// Ignore empty bodies unless we have OOB
	if stanza.StringValue(msg.Body) == "" {
		var reactions XMPPReactions
		if msg.Get(&reactions) && reactions.ID != "" {
			nc.handleIncomingXMPPReaction(msg, timestamp, reactions, isFromMe)
			return
		}
		if nc.handleIncomingXMPPReceipt(msg, timestamp, bareFromJid, isMUC, isFromMe) {
			return
		}
		if attachment != nil {
			msg.Body = stanza.StringPtr(attachment.URL)
		} else {
			nc.log.Debug().
				Str("from", fromJidStr).
				Str("type", string(msg.Type)).
				Bool("from_mam", fromMAM).
				Bool("is_muc", isMUC).
				Str("delay", delayStamp).
				Msg("Skipping XMPP message with empty body")
			return
		}
	}

	portalID := MakePortalID(bareFromJid)
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("raw_jid", bareFromJid).
			Str("portal_id", string(portalID)).
			Str("escaped", EscapeJID(bareFromJid)).
			Msg("Resolved portal ID")
	}

	var senderID string
	var roomType *database.RoomType
	if delayStamp != "" && nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("from", fromJidStr).
			Str("type", string(msg.Type)).
			Bool("from_mam", fromMAM).
			Bool("is_muc", isMUC).
			Str("delay", delayStamp).
			Msg("Received delayed XMPP message")
	}
	if isMUC {
		senderID = nc.resolveMUCSenderID(msg, bareFromJid)
		if senderID == "" {
			// For MUCs, include the room and occupant nickname for uniqueness
			senderID = fromJidStr
		}
		if prefJID, stripped, ok := extractPrefixedSender(stanza.StringValue(msg.Body)); ok {
			if nc.log.GetLevel() <= zerolog.DebugLevel {
				nc.log.Debug().
					Str("room_jid", bareFromJid).
					Str("prefixed_sender", prefJID).
					Str("body_before", stanza.StringValue(msg.Body)).
					Str("body_after", stripped).
					Msg("Using prefixed sender JID from message body")
			}
			senderID = prefJID
			msg.Body = stanza.StringPtr(stripped)
		} else if isMUC {
			if bodyParts := strings.SplitN(stanza.StringValue(msg.Body), " ", 2); len(bodyParts) == 2 && strings.HasPrefix(bodyParts[0], "+") {
				if nc.log.GetLevel() <= zerolog.DebugLevel {
					nc.log.Debug().
						Str("room_jid", bareFromJid).
						Str("phone_prefix", bodyParts[0]).
						Msg("Using phone number prefix as sender")
				}
				senderID = bodyParts[0] + "@cheogram.com"
				msg.Body = stanza.StringPtr(bodyParts[1])
			}
		}
		if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil && meta.RemoteUserID != "" {
			remoteBare := strings.SplitN(meta.RemoteUserID, "/", 2)[0]
			if remoteBare != "" && (senderID == remoteBare || senderID == bareFromJid) {
				isFromMe = true
				if nc.log.GetLevel() <= zerolog.DebugLevel {
					nc.log.Debug().
						Str("room_jid", bareFromJid).
						Str("sender", senderID).
						Msg("Treating MUC message as from self")
				}
			}
		}
	} else {
		// DMs
		senderID = bareFromJid
	}
	if isFromMe {
		if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil && meta.RemoteUserID != "" {
			if !isMUC {
				senderID = meta.RemoteUserID
			}
		}
	}
	if isMUC {
		msg.Body = stanza.StringPtr(stripMUCSenderPrefix(stanza.StringValue(msg.Body), senderID))
	}
	logJIDNormalization(nc.log, "message_sender", senderID)
	if isMUC && !fromMAM {
		if (nc.mucRooms == nil || !nc.mucRooms[bareFromJid]) && nc.isXMPPConnected() {
			start := nc.historySyncStartForPortal(context.Background(), networkid.PortalKey{ID: portalID})
			nc.scheduleMUCBackfill(bareFromJid, start)
			nc.joinMUC(bareFromJid, "", "")
		}
	}

	roomType = ptr.Ptr(database.RoomTypeDefault)
	if !isMUC {
		roomType = ptr.Ptr(database.RoomTypeDM)
		nc.recordDMPortal(string(portalID))
	}

	// First send a ChatResync to create or update the portal with correct info
	resync := &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    networkid.PortalKey{ID: portalID},
			CreatePortal: true,
			Timestamp:    timestamp,
		},
		ChatInfo: &bridgev2.ChatInfo{
			Name: ptr.Ptr(dmRoomName(context.Background(), nc.bridge, bareFromJid, "", nc.contactNameSuffix())),
			Type: roomType,
		},
	}
	nc.log.Debug().Str("portal_id", string(portalID)).Msg("Queueing chat resync for incoming message")
	nc.bridge.QueueRemoteEvent(nc.login, resync)

	msgID := nc.extractMessageID(msg)
	if msgID == "" {
		msgID = idOverride
	}
	serverMsgID := nc.extractServerMessageID(msg)

	evt := &simplevent.Message[string]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: networkid.PortalKey{ID: portalID},
			Sender: bridgev2.EventSender{
				Sender:   MakeUserID(senderID),
				IsFromMe: isFromMe,
			},
			Timestamp: timestamp,
			PostHandleFunc: func(ctx context.Context, portal *bridgev2.Portal) {
				nc.markPortalHistoryProgress(ctx, portal, timestamp, msgID, serverMsgID)
			},
		},
		Data: stanza.StringValue(msg.Body),
		ID:   networkid.MessageID(msgID),
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data string) (*bridgev2.ConvertedMessage, error) {
			// Save the room type metadata inside the ConvertMessageFunc as we have the portal
			meta, _ := portal.Metadata.(*PortalMetadata)
			if meta != nil && meta.IsMUC != isMUC {
				meta.IsMUC = isMUC
				portal.Save(ctx)
			}

			msgType := event.MsgText
			body := data
			var info *event.FileInfo
			var mediaURL string
			var encryptedFile *event.EncryptedFileInfo
			fileName := ""
			contentType := ""
			remoteURL := ""
			caption := ""
			if attachment != nil {
				remoteURL = attachment.URL
				fileName = attachment.FileName
				contentType = attachment.ContentType
				caption = extractXMPPAttachmentCaption(msg, attachment)
				if caption != "" {
					body = caption
				}
			} else {
				body = mergeXMPPSubjectAndBody(stanza.StringValue(msg.Subject), body)
			}
			if remoteURL == "" && (strings.HasPrefix(data, "http://") || strings.HasPrefix(data, "https://")) {
				remoteURL = data
			}
			if remoteURL != "" {
				if u, err := url.Parse(remoteURL); err == nil && u.Scheme != "" && u.Host != "" {
					const maxMediaSize = 50 * 1024 * 1024
					client := &http.Client{Timeout: 60 * time.Second}
					getResp, err := client.Get(remoteURL)
					if err == nil && getResp != nil {
						defer getResp.Body.Close()
						if getResp.StatusCode == http.StatusOK {
							if contentType == "" {
								contentType = getResp.Header.Get("Content-Type")
							}
							limited := io.LimitReader(getResp.Body, maxMediaSize+1)
							dataBytes, err := io.ReadAll(limited)
							if err == nil && int64(len(dataBytes)) <= maxMediaSize {
								if contentType == "" {
									contentType = http.DetectContentType(dataBytes)
								}
								candidateType := classifyContentTypeMessageType(contentType, attachment != nil)
								if candidateType != event.MsgText {
									if fileName == "" {
										fileName = path.Base(u.Path)
										if fileName == "" || fileName == "/" || fileName == "." {
											fileName = "file"
										}
									}
									mediaMxc, uploadedFile, err := intent.UploadMedia(ctx, portal.MXID, dataBytes, fileName, contentType)
									if err == nil {
										mediaURL = string(mediaMxc)
										encryptedFile = uploadedFile
										msgType = candidateType
										if body == "" {
											body = fileName
										}
										info = &event.FileInfo{
											MimeType: contentType,
											Size:     len(dataBytes),
										}
										if attachment != nil {
											info.Width = attachment.Width
											info.Height = attachment.Height
											info.Duration = attachment.DurationMS
											if attachment.Size > 0 {
												info.Size = attachment.Size
											}
										}
									}
								}
							}
						}
					}
					if mediaURL == "" && attachment != nil {
						if body == "" {
							body = remoteURL
						} else if body != remoteURL && !strings.Contains(body, remoteURL) {
							body = body + "\n" + remoteURL
						}
					}
				}
			}

			content := &event.MessageEventContent{
				MsgType: msgType,
				Body:    body,
			}
			if mediaURL != "" || encryptedFile != nil {
				if body == "" && fileName != "" {
					content.Body = fileName
				}
				if mediaURL != "" {
					content.URL = id.ContentURIString(mediaURL)
				}
				content.File = encryptedFile
				content.Info = info
				if fileName != "" && content.Body != fileName {
					content.FileName = fileName
				}
				if msgType == event.MsgAudio && info != nil && info.Duration > 0 {
					content.Body = preferredXMPPVoiceNoteBody(msg, attachment, content.Body)
					content.MSC1767Audio = &event.MSC1767Audio{Duration: info.Duration}
					if isLikelyXMPPVoiceNote(msg, attachment) {
						content.MSC3245Voice = &event.MSC3245Voice{}
					}
				}
			}
			applyMatrixLinkFormatting(content)

			return &bridgev2.ConvertedMessage{
				ReplyTo: func() *networkid.MessageOptionalPartID {
					if replyTargetID == "" {
						return nil
					}
					return &networkid.MessageOptionalPartID{MessageID: networkid.MessageID(replyTargetID)}
				}(),
				Parts: []*bridgev2.ConvertedMessagePart{{
					Type:    event.EventMessage,
					Content: content,
				}},
			}, nil
		},
	}

	// Ensure that if it has no ID, we generate one
	if evt.ID == "" {
		evt.ID = networkid.MessageID(fmt.Sprintf("remote-%s-%d", portalID, time.Now().UnixNano()))
	}

	nc.log.Debug().
		Str("portal_id", string(portalID)).
		Str("sender_id", senderID).
		Str("sender_escaped", EscapeJID(senderID)).
		Str("message_id", string(evt.ID)).
		Msg("Queueing incoming XMPP message")
	nc.bridge.QueueRemoteEvent(nc.login, evt)
}

func (nc *MyNetworkClient) handleIncomingXMPPReaction(msg stanza.Message, timestamp time.Time, reactions XMPPReactions, isFromMe bool) {
	if reactions.ID == "" {
		nc.log.Debug().Str("from", msg.From).Msg("Skipping XMPP reaction without target ID")
		return
	}

	fromParts := strings.Split(msg.From, "/")
	bareFromJid := fromParts[0]
	if bareFromJid == "" {
		return
	}
	isMUC := msg.Type == stanza.MessageTypeGroupchat
	if strings.Contains(bareFromJid, ",") && strings.HasSuffix(bareFromJid, "@cheogram.com") {
		isMUC = true
	}

	senderID := bareFromJid
	if isMUC {
		senderID = nc.resolveMUCSenderID(msg, bareFromJid)
		if senderID == "" {
			senderID = msg.From
		}
	}
	if isFromMe {
		if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil && meta.RemoteUserID != "" {
			senderID = meta.RemoteUserID
		}
	}

	portalID := MakePortalID(bareFromJid)
	roomType := ptr.Ptr(database.RoomTypeDefault)
	if !isMUC {
		roomType = ptr.Ptr(database.RoomTypeDM)
	}
	resync := &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    networkid.PortalKey{ID: portalID},
			CreatePortal: true,
			Timestamp:    timestamp,
		},
		ChatInfo: &bridgev2.ChatInfo{
			Name: ptr.Ptr(dmRoomName(context.Background(), nc.bridge, bareFromJid, "", nc.contactNameSuffix())),
			Type: roomType,
		},
	}
	nc.bridge.QueueRemoteEvent(nc.login, resync)
	reactionList := reactions.All()
	reactionItems := make([]*bridgev2.BackfillReaction, 0, len(reactionList))
	sender := bridgev2.EventSender{
		Sender:   MakeUserID(senderID),
		IsFromMe: isFromMe,
	}
	for _, emoji := range reactionList {
		reactionItems = append(reactionItems, &bridgev2.BackfillReaction{
			Timestamp: timestamp,
			Sender:    sender,
			EmojiID:   networkid.EmojiID(emoji),
			Emoji:     emoji,
		})
	}

	nc.bridge.QueueRemoteEvent(nc.login, &simplevent.ReactionSync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReactionSync,
			PortalKey: networkid.PortalKey{ID: portalID},
			Sender:    sender,
			Timestamp: timestamp,
		},
		TargetMessage: networkid.MessageID(reactions.ID),
		Reactions: &bridgev2.ReactionSyncData{
			Users: map[networkid.UserID]*bridgev2.ReactionSyncUser{
				MakeUserID(senderID): &bridgev2.ReactionSyncUser{
					Reactions:       reactionItems,
					HasAllReactions: true,
				},
			},
		},
	})
}

func (nc *MyNetworkClient) extractMessageID(msg stanza.Message) string {
	var origin XMPPOriginID
	if msg.Get(&origin) && origin.ID != "" {
		return origin.ID
	}
	var stanzaID XMPPStanzaID
	if msg.Get(&stanzaID) && stanzaID.ID != "" {
		return stanzaID.ID
	}
	if msg.Id != "" {
		return msg.Id
	}
	return ""
}

func (nc *MyNetworkClient) extractServerMessageID(msg stanza.Message) string {
	var stanzaID XMPPStanzaID
	if msg.Get(&stanzaID) && stanzaID.ID != "" {
		return stanzaID.ID
	}
	return ""
}

func (nc *MyNetworkClient) resolveMUCSenderID(msg stanza.Message, roomJID string) string {
	var mucUser MUCUserInvite
	if msg.Get(&mucUser) && mucUser.Item != nil {
		if jid := bareJID(strings.TrimSpace(mucUser.Item.JID)); jid != "" {
			return jid
		}
		if nick := strings.TrimSpace(mucUser.Item.Nick); nick != "" {
			if strings.Contains(roomJID, ",") && strings.HasSuffix(roomJID, "@cheogram.com") && strings.HasPrefix(nick, "+") && !strings.Contains(nick, "@") {
				return nick + "@cheogram.com"
			}
			return roomJID + "/" + nick
		}
	}
	var occ XMPOccupantID
	if msg.Get(&occ) && occ.ID != "" {
		return roomJID + "/occ-" + occ.ID
	}
	if strings.Contains(msg.From, "/") {
		return msg.From
	}
	return ""
}

func stripMUCSenderPrefix(body string, senderID string) string {
	if body == "" || senderID == "" {
		return body
	}
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return body
	}
	prefixes := []string{
		"<xmpp:" + senderID + ">",
		"xmpp:" + senderID + ":",
		senderID + ":",
	}
	if parts := strings.Split(senderID, "/"); len(parts) > 1 && parts[len(parts)-1] != "" {
		nick := parts[len(parts)-1]
		prefixes = append(prefixes, nick+":")
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	return body
}

func extractPrefixedSender(body string) (string, string, bool) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return "", body, false
	}
	start := strings.Index(trimmed, "<xmpp:")
	if start == -1 {
		return "", body, false
	}
	end := strings.Index(trimmed[start:], ">")
	if end == -1 {
		return "", body, false
	}
	end = start + end
	jid := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed[start:end+1], "<xmpp:"), ">"))
	if jid == "" || !strings.Contains(jid, "@") {
		return "", body, false
	}
	remainder := strings.TrimSpace(trimmed[end+1:])
	return jid, remainder, true
}

func (nc *MyNetworkClient) maybeClearFullSync() {
	nc.syncMu.Lock()
	defer nc.syncMu.Unlock()
	if !nc.fullSyncRequested {
		return
	}
	if nc.fullSyncRosterPending || nc.fullSyncBookmarksPending || nc.fullSyncAccountDiscoPending || nc.fullSyncBackfillsPending > 0 {
		return
	}
	nc.fullSyncRequested = false
}

func (nc *MyNetworkClient) trackFullSyncBackfill() bool {
	nc.syncMu.Lock()
	defer nc.syncMu.Unlock()
	if !nc.fullSyncRequested {
		return false
	}
	nc.fullSyncBackfillsPending++
	return true
}

func (nc *MyNetworkClient) finishTrackedFullSyncBackfill(tracked bool) {
	if !tracked {
		return
	}
	nc.syncMu.Lock()
	if nc.fullSyncBackfillsPending > 0 {
		nc.fullSyncBackfillsPending--
	}
	nc.syncMu.Unlock()
	nc.maybeClearFullSync()
}

func (nc *MyNetworkClient) WaitForConnected(ctx context.Context) bool {
	nc.connectMu.Lock()
	if nc.connected {
		nc.connectMu.Unlock()
		return true
	}
	connectedCh := nc.connectedCh
	nc.connectMu.Unlock()
	if connectedCh == nil {
		return false
	}
	select {
	case <-connectedCh:
		return true
	case <-ctx.Done():
		return false
	}
}

func (nc *MyNetworkClient) StartFullSync(ctx context.Context, notify func(string), full bool, lookback time.Duration) error {
	nc.syncMu.Lock()
	if nc.syncRunning {
		status := nc.syncStatus
		nc.syncMu.Unlock()
		if status == "" {
			status = "in progress"
		}
		return fmt.Errorf("sync already running (%s)", status)
	}
	nc.syncRunning = true
	nc.syncStatus = "queued"
	nc.syncLastErr = ""
	nc.syncStartedAt = time.Now()
	nc.fullSyncBackfillsPending = 0
	nc.fullSyncAccountDiscoPending = false
	nc.dmBackfillPending = map[string]time.Time{}
	if full {
		nc.fullSyncUseStart = false
		nc.fullSyncStart = time.Time{}
	} else {
		if lookback <= 0 {
			lookback = defaultMAMLookback
		}
		nc.fullSyncUseStart = true
		nc.fullSyncStart = time.Now().Add(-lookback)
	}
	nc.resetSyncStats()
	nc.mamSupportMu.Lock()
	nc.mamServerUnsupportedNotified = false
	if nc.mamMUCUnsupportedNotified == nil {
		nc.mamMUCUnsupportedNotified = map[string]bool{}
	} else {
		for key := range nc.mamMUCUnsupportedNotified {
			delete(nc.mamMUCUnsupportedNotified, key)
		}
	}
	if nc.mamMUCUnsupportedPendingSync == nil {
		nc.mamMUCUnsupportedPendingSync = map[string]bool{}
	} else {
		for key := range nc.mamMUCUnsupportedPendingSync {
			delete(nc.mamMUCUnsupportedPendingSync, key)
		}
	}
	nc.mamSupportMu.Unlock()
	nc.syncMu.Unlock()

	go nc.runFullSync(ctx, notify)
	return nil
}

func (nc *MyNetworkClient) SyncStatus() (bool, string, string, time.Time) {
	nc.syncMu.Lock()
	defer nc.syncMu.Unlock()
	return nc.syncRunning, nc.syncStatus, nc.syncLastErr, nc.syncStartedAt
}

func (nc *MyNetworkClient) setSyncStatus(status string) {
	nc.syncMu.Lock()
	nc.syncStatus = status
	nc.syncMu.Unlock()
}

func (nc *MyNetworkClient) setSyncError(err error) {
	nc.syncMu.Lock()
	if err != nil {
		nc.syncLastErr = err.Error()
	}
	nc.syncMu.Unlock()
}

func (nc *MyNetworkClient) finishSync() {
	nc.syncMu.Lock()
	nc.syncRunning = false
	nc.syncMu.Unlock()
}

func (nc *MyNetworkClient) runFullSync(ctx context.Context, notify func(string)) {
	defer nc.finishSync()
	update := func(msg string) {
		nc.setSyncStatus(msg)
		if notify != nil {
			notify(msg)
		}
	}

	update("Waiting for XMPP connection")
	connectCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	if !nc.WaitForConnected(connectCtx) {
		cancel()
		err := fmt.Errorf("XMPP connection timed out")
		nc.setSyncError(err)
		if notify != nil {
			notify(fmt.Sprintf("Sync failed: %s", err.Error()))
		}
		nc.reportSyncError(fmt.Sprintf("Sync failed: %s", err.Error()))
		nc.notifyManagement(nc.syncSummaryText("failed"))
		return
	}
	cancel()

	update("Requesting roster and bookmarks")
	nc.fullSyncRequested = true
	nc.fullSyncRosterPending = true
	nc.fullSyncBookmarksPending = true
	nc.fullSyncAccountDiscoPending = nc.ownBareJID() != ""
	nc.requestOwnDiscoInfo()
	if err := nc.requestRoster(); err != nil {
		nc.setSyncError(err)
		if notify != nil {
			notify(fmt.Sprintf("Sync failed requesting roster: %s", err.Error()))
		}
		nc.reportSyncError(fmt.Sprintf("Sync failed requesting roster: %s", err.Error()))
		nc.notifyManagement(nc.syncSummaryText("failed"))
		return
	}
	if err := nc.requestBookmarks(); err != nil {
		nc.setSyncError(err)
		if notify != nil {
			notify(fmt.Sprintf("Sync failed requesting bookmarks: %s", err.Error()))
		}
		nc.reportSyncError(fmt.Sprintf("Sync failed requesting bookmarks: %s", err.Error()))
		nc.notifyManagement(nc.syncSummaryText("failed"))
		return
	}

	update("Backfill in progress (MAM)")
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		if !nc.fullSyncRequested {
			update("Sync complete")
			nc.notifyManagement(nc.syncSummaryText("complete"))
			go nc.connector.ensureSpaceRoomAndPopulate(nc.login)
			return
		}
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			err := ctx.Err()
			nc.setSyncError(err)
			if notify != nil {
				notify(fmt.Sprintf("Sync cancelled: %s", err.Error()))
			}
			nc.reportSyncError(fmt.Sprintf("Sync cancelled: %s", err.Error()))
			nc.notifyManagement(nc.syncSummaryText("cancelled"))
			return
		}
	}

	nc.fullSyncRequested = false
	update("Sync timed out (backfill may still be running)")
	nc.notifyManagement(nc.syncSummaryText("timed out"))
	go nc.connector.ensureSpaceRoomAndPopulate(nc.login)
}

// Disconnect cleans up the XMPP connection.
func (nc *MyNetworkClient) Disconnect() {
	nc.log.Info().Msg("MyNetworkClient Disconnect called")
	nc.connectMu.Lock()
	if nc.stopReconnect != nil {
		close(nc.stopReconnect)
		nc.stopReconnect = nil
	}
	nc.connectStarted = false
	nc.reconnectAttempt = 0
	nc.connected = false
	client := nc.xmppClient
	nc.connectMu.Unlock()
	nc.postConnectMu.Lock()
	nc.sessionEstablishedAt = time.Time{}
	nc.postConnectMu.Unlock()
	if client != nil {
		client.Disconnect()
	}
}

// LogoutRemote logs the user out.
func (nc *MyNetworkClient) LogoutRemote(ctx context.Context) {
	nc.log.Info().Msg("MyNetworkClient LogoutRemote called")
	nc.Disconnect()
}

// IsThisUser checks if the given remote network user ID belongs to this client instance.
func (nc *MyNetworkClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	meta, ok := nc.login.Metadata.(*LoginMetadata)
	if !ok || meta == nil {
		return false
	}
	bareUserID := strings.Split(UnescapeJID(string(userID)), "/")[0]
	return bareUserID == meta.RemoteUserID
}

// IsLoggedIn checks if we have a valid access token.
func (nc *MyNetworkClient) IsLoggedIn() bool {
	meta, ok := nc.login.Metadata.(*LoginMetadata)
	return ok && meta != nil && meta.AccessToken != ""
}
