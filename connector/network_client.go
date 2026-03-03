package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type CarbonsEnable struct {
	XMLName xml.Name `xml:"urn:xmpp:carbons:2 enable"`
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

func (c *CarbonsEnable) Namespace() string {
	return "urn:xmpp:carbons:2"
}

func (c *CarbonsEnable) GetSet() *stanza.ResultSet {
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

	xmppClient  *xmpp.Client
	xmppRouter  *xmpp.Router
	xmppLogFile *os.File

	httpUploadServiceJID         string
	mucRooms                     map[string]bool
	mucOccupants                 map[string]map[string]bool
	mucNick                      map[string]string
	mucPasswords                 map[string]string
	mucBackfillPending           map[string]time.Time
	mucBackfillMu                sync.Mutex
	mucServices                  map[string]bool
	mucServiceDiscoRequested     map[string]bool
	mucHistoryRequests           map[string]stanza.History
	mucHistoryFallbackAttempts   map[string]bool
	mucHistoryMu                 sync.Mutex
	discoFeatures                map[string]map[string]bool
	discoMu                      sync.Mutex
	mamSupportMu                 sync.Mutex
	mamServerUnsupportedNotified bool
	mamMUCUnsupportedNotified    map[string]bool
	rosterItems                  map[string]bool
	bookmarkedRooms              map[string]bool
	fullSyncRequested            bool
	fullSyncRosterPending        bool
	fullSyncBookmarksPending     bool
	fullSyncStart                time.Time
	fullSyncUseStart             bool
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
}

const defaultMAMLookback = 30 * 24 * time.Hour

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

func (nc *MyNetworkClient) requestBookmarks() error {
	if nc.xmppClient == nil {
		return fmt.Errorf("XMPP client not connected")
	}
	if err := nc.requestBookmarks2(); err != nil {
		nc.log.Err(err).Msg("Failed to send bookmarks2 request")
	}
	bookmarksIq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		Id:   fmt.Sprintf("bookmarks_%d", time.Now().UnixNano()),
	})
	bookmarksIq.Payload = &PrivateBookmarkQuery{Storage: &BookmarkStorage{}}
	if err := nc.xmppClient.Send(bookmarksIq); err != nil {
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
	if nc.xmppClient == nil {
		return fmt.Errorf("XMPP client not connected")
	}
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
	if err := nc.xmppClient.Send(bookmarksIq); err != nil {
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
	if nc.xmppClient == nil {
		return fmt.Errorf("XMPP client not connected")
	}
	iq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		Id:   fmt.Sprintf("roster_%d", time.Now().UnixNano()),
	})
	iq.Payload = &stanza.Roster{}
	if err := nc.xmppClient.Send(iq); err != nil {
		return err
	}
	return nil
}

func (nc *MyNetworkClient) joinMUC(roomJID, nickname, password string) {
	if nc.xmppClient == nil {
		nc.log.Error().Str("room_jid", roomJID).Msg("Cannot join MUC: XMPP client is nil")
		return
	}
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

	if err := nc.xmppClient.Send(pres); err != nil {
		nc.log.Err(err).Str("room_jid", roomJID).Str("nick", nickname).Msg("Failed to join MUC")
		return
	}

	nc.log.Info().Str("room_jid", roomJID).Str("nick", nickname).Msg("Sent MUC join presence")
}

func (nc *MyNetworkClient) scheduleMUCBackfill(roomJID string, start time.Time) {
	supported, known := nc.mamSupportedForMUCService(roomJID)
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("room_jid", roomJID).
			Str("service", jidDomain(roomJID)).
			Bool("mam_supported", supported).
			Bool("mam_known", known).
			Time("start", start).
			Msg("Evaluating MUC MAM support")
	}
	if !known {
		nc.scheduleMUCHistoryFallback(roomJID, start)
		if nc.log.GetLevel() <= zerolog.DebugLevel {
			nc.log.Debug().
				Str("room_jid", roomJID).
				Time("start", start).
				Msg("MUC MAM support unknown; using history fallback")
		}
		return
	}
	if !supported {
		_ = nc.ensureMAMSupportForMUC(roomJID)
		nc.scheduleMUCHistoryFallback(roomJID, start)
		return
	}
	nc.mucBackfillMu.Lock()
	if nc.mucBackfillPending == nil {
		nc.mucBackfillPending = map[string]time.Time{}
	}
	nc.mucBackfillPending[roomJID] = start
	nc.mucBackfillMu.Unlock()
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("room_jid", roomJID).
			Time("start", start).
			Bool("start_zero", start.IsZero()).
			Msg("Scheduled MUC MAM backfill")
	}
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
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().Str("source", source).Int("items", len(confs)).Msg("Processing bookmarks")
	}
	for _, conf := range confs {
		roomJID := strings.TrimSpace(conf.JID)
		if roomJID == "" {
			continue
		}
		logJIDNormalization(nc.log, "bookmark_room", roomJID)
		if nc.log.GetLevel() <= zerolog.DebugLevel {
			nc.log.Debug().
				Str("source", source).
				Str("room_jid", roomJID).
				Str("autojoin", conf.Autojoin).
				Str("nick", strings.TrimSpace(conf.Nick)).
				Msg("Found bookmarked room")
		}
		if nc.bookmarkedRooms != nil {
			nc.bookmarkedRooms[roomJID] = true
		}
		name := conf.Name
		if name == "" {
			name = roomJID
		}

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
		start := time.Time{}
		if nc.fullSyncRequested {
			if nc.fullSyncUseStart {
				start = nc.fullSyncStart
			}
		} else {
			start = time.Now().Add(-defaultMAMLookback)
		}
		nc.scheduleMUCBackfill(roomJID, start)
		nc.joinMUC(roomJID, nick, conf.Password)
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
	if serverDomain == "" || nc.xmppClient == nil {
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
		if err := nc.xmppClient.Send(infoIq); err != nil {
			nc.log.Err(err).Str("to", candidate).Msg("Failed to probe disco info")
		} else if nc.log.GetLevel() <= zerolog.DebugLevel {
			nc.log.Debug().Str("to", candidate).Msg("Probing disco info")
		}
		itemsIq, _ := stanza.NewIQ(stanza.Attrs{
			Type: stanza.IQTypeGet,
			To:   candidate,
			Id:   fmt.Sprintf("disco_items_probe_%d", time.Now().UnixNano()),
		})
		itemsIq.Payload = &stanza.DiscoItems{}
		if err := nc.xmppClient.Send(itemsIq); err != nil {
			nc.log.Err(err).Str("to", candidate).Msg("Failed to probe disco items")
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

func (nc *MyNetworkClient) mamSupportedForServer() (bool, bool) {
	remote := ""
	if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil {
		remote = meta.RemoteUserID
	}
	server := jidDomain(remote)
	return nc.hasDiscoFeature(server, NSMAM)
}

func (nc *MyNetworkClient) mamSupportedForMUCService(roomJID string) (bool, bool) {
	service := jidDomain(roomJID)
	return nc.hasDiscoFeature(service, NSMAM)
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
	supported, known := nc.mamSupportedForMUCService(roomJID)
	if !known || supported {
		return true
	}
	service := jidDomain(roomJID)
	nc.mamSupportMu.Lock()
	if nc.mamMUCUnsupportedNotified == nil {
		nc.mamMUCUnsupportedNotified = map[string]bool{}
	}
	if !nc.mamMUCUnsupportedNotified[service] {
		nc.mamMUCUnsupportedNotified[service] = true
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
	nc.log.Info().Msg("MyNetworkClient Connect called")
	if nc.mucRooms == nil {
		nc.mucRooms = map[string]bool{}
	}
	if nc.mucOccupants == nil {
		nc.mucOccupants = map[string]map[string]bool{}
	}
	if nc.mucNick == nil {
		nc.mucNick = map[string]string{}
	}
	if nc.mucBackfillPending == nil {
		nc.mucBackfillPending = map[string]time.Time{}
	}
	if nc.mucServices == nil {
		nc.mucServices = map[string]bool{}
	}
	if nc.mucServiceDiscoRequested == nil {
		nc.mucServiceDiscoRequested = map[string]bool{}
	}
	if nc.mucHistoryRequests == nil {
		nc.mucHistoryRequests = map[string]stanza.History{}
	}
	if nc.mamMUCUnsupportedNotified == nil {
		nc.mamMUCUnsupportedNotified = map[string]bool{}
	}
	if nc.rosterItems == nil {
		nc.rosterItems = map[string]bool{}
	}
	if nc.bookmarkedRooms == nil {
		nc.bookmarkedRooms = map[string]bool{}
	}
	nc.connected = false
	nc.connectedCh = make(chan struct{})

	meta, ok := nc.login.Metadata.(*LoginMetadata)
	if !ok || meta == nil {
		nc.log.Error().Msg("LoginMetadata is missing or invalid")
		return
	}

	config := xmpp.Config{
		TransportConfiguration: xmpp.TransportConfiguration{},
		Jid:                    meta.RemoteUserID,
		Credential:             xmpp.Password(meta.AccessToken),
		Insecure:               false,
		StreamManagementEnable: true,
	}

	if nc.log.GetLevel() <= zerolog.DebugLevel {
		if err := os.MkdirAll("logs", 0o755); err != nil {
			nc.log.Err(err).Msg("Failed to create logs directory for XMPP traffic")
		} else {
			logFile, err := os.OpenFile("logs/xmpp-traffic.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				nc.log.Err(err).Msg("Failed to open XMPP traffic log file")
			} else {
				config.StreamLogger = logFile
				nc.xmppLogFile = logFile
				nc.log.Warn().Msg("XMPP traffic logging enabled (logs/xmpp-traffic.log)")
			}
		}

		config.Insecure = true
		nc.log.Warn().Msg("XMPP insecure mode enabled for debug logging")
	}

	nc.xmppRouter = xmpp.NewRouter()
	nc.xmppRouter.HandleFunc("message", nc.handleXMPPMessage)
	nc.xmppRouter.HandleFunc("presence", nc.handleXMPPPresence)
	nc.xmppRouter.HandleFunc("iq", nc.handleXMPPIq)

	client, err := xmpp.NewClient(&config, nc.xmppRouter, func(err error) {
		nc.log.Err(err).Msg("XMPP Error")
	})
	if err != nil {
		nc.log.Err(err).Msg("Failed to create XMPP client")
		return
	}

	nc.xmppClient = client
	nc.bridge.Log.Info().
		Str("jid", config.Jid).
		Bool("insecure", config.Insecure).
		Bool("stream_logger", config.StreamLogger != nil).
		Msg("Connecting to XMPP...")

	client.PostConnectHook = func() error {
		nc.log.Info().Msg("XMPP connected, running post connect hook")
		if nc.xmppClient == nil {
			return fmt.Errorf("XMPP client unavailable in post connect hook")
		}
		if !nc.connected {
			nc.connected = true
			if nc.connectedCh != nil {
				close(nc.connectedCh)
			}
		}
		if !nc.connected {
			nc.connected = true
			if nc.connectedCh != nil {
				close(nc.connectedCh)
			}
		}

		// 1. Request Roster
		iq, _ := stanza.NewIQ(stanza.Attrs{
			Type: stanza.IQTypeGet,
			Id:   "roster_1",
		})
		iq.Payload = &stanza.Roster{}
		if err := nc.xmppClient.Send(iq); err != nil {
			nc.log.Err(err).Msg("Failed to send roster request")
		} else {
			nc.log.Debug().Msg("Sent roster request")
		}

		// 2. Request Server Disco Info
		if nc.xmppClient.Session != nil {
			bindJid := nc.xmppClient.Session.BindJid
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
				Id:   "disco_1",
			})
			discoIq.Payload = &stanza.DiscoInfo{}
			if err := nc.xmppClient.Send(discoIq); err != nil {
				nc.log.Err(err).Msg("Failed to send disco info request")
			} else {
				nc.log.Debug().Str("to", serverDomain).Msg("Sent disco info request")
			}

			discoItemsIq, _ := stanza.NewIQ(stanza.Attrs{
				Type: stanza.IQTypeGet,
				To:   serverDomain,
				Id:   "disco_items_1",
			})
			discoItemsIq.Payload = &stanza.DiscoItems{}
			if err := nc.xmppClient.Send(discoItemsIq); err != nil {
				nc.log.Err(err).Msg("Failed to send disco items request")
			} else {
				nc.log.Debug().Str("to", serverDomain).Msg("Sent disco items request")
			}
			nc.probeMUCServices(serverDomain)
		}

		// 3. Enable Message Carbons
		carbonsIq, _ := stanza.NewIQ(stanza.Attrs{
			Type: stanza.IQTypeSet,
			Id:   "carbons_1",
		})
		carbonsIq.Payload = &CarbonsEnable{}
		if err := nc.xmppClient.Send(carbonsIq); err != nil {
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

		// Initial Presence is sent automatically by the library

		return nil
	}

	// Run in goroutine
	go func() {
		// client.Connect() blocks until disconnection
		err := client.Connect()
		if err != nil {
			nc.log.Err(err).Msg("Failed to establish XMPP connection")
		} else {
			nc.log.Warn().Msg("XMPP connection ended without error")
		}
	}()
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
	if bareFrom != "" && nc.mucRooms != nil && nc.mucRooms[bareFrom] {
		resource := ""
		if parts := strings.SplitN(pres.From, "/", 2); len(parts) == 2 {
			resource = parts[1]
		}
		if pres.Type != stanza.PresenceTypeUnavailable {
			if nick, ok := nc.mucNick[bareFrom]; ok && nick != "" && resource == nick {
				if start, ok := nc.popMUCBackfill(bareFrom); ok {
					if nc.log.GetLevel() <= zerolog.DebugLevel {
						nc.log.Debug().
							Str("room_jid", bareFrom).
							Str("nick", nick).
							Time("start", start).
							Bool("start_zero", start.IsZero()).
							Msg("Starting MUC MAM backfill after join")
					}
					nc.recordMAMBackfillMUC()
					go func(roomJID string, start time.Time) {
						if err := nc.backfillMUC(context.Background(), roomJID, start); err != nil {
							nc.log.Err(err).Str("room_jid", roomJID).Msg("Failed to backfill MUC MAM history")
							nc.reportSyncError(fmt.Sprintf("MUC backfill failed for %s: %v", roomJID, err))
						}
					}(bareFrom, start)
				}
			}
		}
		if nc.mucOccupants == nil {
			nc.mucOccupants = map[string]map[string]bool{}
		}
		occupants := nc.mucOccupants[bareFrom]
		if occupants == nil {
			occupants = map[string]bool{}
			nc.mucOccupants[bareFrom] = occupants
		}
		if pres.Type == stanza.PresenceTypeUnavailable {
			delete(occupants, pres.From)
		} else if pres.From != "" && strings.Contains(pres.From, "/") {
			occupants[pres.From] = true
		}

		memberMap := bridgev2.ChatMemberMap{}
		for occupant := range occupants {
			memberMap[MakeUserID(occupant)] = bridgev2.ChatMember{
				EventSender: bridgev2.EventSender{Sender: MakeUserID(occupant)},
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
				if err := nc.xmppClient.Send(discoIq); err != nil {
					nc.log.Err(err).Str("to", bareJID).Msg("Failed to send disco info for roster item")
				}
				if nc.rosterItems != nil {
					nc.rosterItems[bareJID] = true
				}
				name := item.Name
				if name == "" {
					name = bareJID
				}
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
					if nc.ensureMAMSupportForServer() {
						start := time.Time{}
						if nc.fullSyncUseStart {
							start = nc.fullSyncStart
						}
						nc.recordMAMBackfillDM()
						if err := nc.backfillDM(context.Background(), bareJID, start); err != nil {
							nc.log.Err(err).Str("jid", bareJID).Msg("Failed to request DM MAM history")
							nc.reportSyncError(fmt.Sprintf("DM backfill failed for %s: %v", bareJID, err))
						}
					}
				}
			}
			if nc.fullSyncRosterPending {
				nc.fullSyncRosterPending = false
				nc.maybeClearFullSync()
			}
		case *stanza.Roster:
			nc.log.Debug().Msg("Received roster result without items")
		case *BookmarkStorage:
			nc.log.Info().Int("items", len(payload.Conferences)).Str("from", string(iq.From)).Msg("Received bookmarks")
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
			nc.handleBookmarkConferences(confs, "bookmarks2")
		case *stanza.DiscoItems:
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
							Name: ptr.Ptr(roomJID),
							Type: ptr.Ptr(database.RoomTypeDefault),
						},
					}
					nc.log.Debug().Str("portal_id", string(portalID)).Msg("Queueing discovered groupchat resync")
					nc.bridge.QueueRemoteEvent(nc.login, resync)
					nc.recordMUCDiscoPortal(string(portalID))
					start := time.Now().Add(-defaultMAMLookback)
					if nc.fullSyncRequested && nc.fullSyncUseStart {
						start = nc.fullSyncStart
					}
					nc.scheduleMUCBackfill(roomJID, start)
					nc.joinMUC(roomJID, "", "")
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
				if err := nc.xmppClient.Send(discoIq); err != nil {
					nc.log.Err(err).Str("to", item.JID).Msg("Failed to send disco info request")
				}
			}
		case *stanza.DiscoInfo:
			nc.recordDiscoFeatures(iq.From, payload.Features)
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
					if err := nc.xmppClient.Send(discoIq); err != nil {
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
		nc.handleIncomingXMPPMessage(fwd, timestamp, idOverride, isFromMe, true)
		return
	}

	var carbonSent XMPCarbonSent
	if msg.Get(&carbonSent) && carbonSent.Forwarded != nil {
		if forwarded, ok := carbonSent.Forwarded.Stanza.(stanza.Message); ok {
			nc.handleIncomingXMPPMessage(forwarded, time.Now(), "", true, false)
			return
		}
	}
	var carbonReceived XMPCarbonReceived
	if msg.Get(&carbonReceived) && carbonReceived.Forwarded != nil {
		if forwarded, ok := carbonReceived.Forwarded.Stanza.(stanza.Message); ok {
			nc.handleIncomingXMPPMessage(forwarded, time.Now(), "", false, false)
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
			roomName := roomJID
			resync := &simplevent.ChatResync{
				EventMeta: simplevent.EventMeta{
					Type:         bridgev2.RemoteEventChatResync,
					PortalKey:    networkid.PortalKey{ID: portalID},
					CreatePortal: true,
					Timestamp:    time.Now(),
				},
				ChatInfo: &bridgev2.ChatInfo{
					Name: &roomName,
					Type: ptr.Ptr(database.RoomTypeDefault),
				},
			}
			nc.log.Info().
				Str("room_jid", roomJID).
				Str("from", mucInvite.Invite.From).
				Str("reason", mucInvite.Invite.Reason).
				Msg("Received MUC invite")
			nc.bridge.QueueRemoteEvent(nc.login, resync)
			nc.scheduleMUCBackfill(roomJID, time.Now().Add(-defaultMAMLookback))
			nc.joinMUC(roomJID, "", mucInvite.Password)
			if msg.Body == "" {
				return
			}
		}
	}

	var directInvite MUCDirectInvite
	if msg.Get(&directInvite) && directInvite.JID != "" {
		roomJID := strings.TrimSpace(directInvite.JID)
		if roomJID != "" {
			portalID := MakePortalID(roomJID)
			roomName := roomJID
			resync := &simplevent.ChatResync{
				EventMeta: simplevent.EventMeta{
					Type:         bridgev2.RemoteEventChatResync,
					PortalKey:    networkid.PortalKey{ID: portalID},
					CreatePortal: true,
					Timestamp:    time.Now(),
				},
				ChatInfo: &bridgev2.ChatInfo{
					Name: &roomName,
					Type: ptr.Ptr(database.RoomTypeDefault),
				},
			}
			nc.log.Info().
				Str("room_jid", roomJID).
				Str("from", directInvite.From).
				Str("reason", directInvite.Reason).
				Msg("Received direct MUC invite")
			nc.bridge.QueueRemoteEvent(nc.login, resync)
			nc.scheduleMUCBackfill(roomJID, time.Now().Add(-defaultMAMLookback))
			nc.joinMUC(roomJID, "", directInvite.Password)
			if msg.Body == "" {
				return
			}
		}
	}

	nc.log.Info().
		Str("from", fromJidStr).
		Str("type", string(msg.Type)).
		Str("body", msg.Body).
		Msg("Received XMPP message")
	nc.log.Debug().Interface("message", msg).Msg("Message details")

	nc.handleIncomingXMPPMessage(msg, time.Now(), "", false, false)
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

	isMUC := msg.Type == stanza.MessageTypeGroupchat
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

	// Ignore empty bodies unless we have OOB
	if msg.Body == "" {
		var oob stanza.OOB
		if msg.Get(&oob) && oob.URL != "" {
			msg.Body = oob.URL
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
		if prefJID, stripped, ok := extractPrefixedSender(msg.Body); ok {
			if nc.log.GetLevel() <= zerolog.DebugLevel {
				nc.log.Debug().
					Str("room_jid", bareFromJid).
					Str("prefixed_sender", prefJID).
					Str("body_before", msg.Body).
					Str("body_after", stripped).
					Msg("Using prefixed sender JID from message body")
			}
			senderID = prefJID
			msg.Body = stripped
		}
		roomType = ptr.Ptr(database.RoomTypeDefault)
	} else {
		// DMs
		senderID = bareFromJid
		roomType = ptr.Ptr(database.RoomTypeDM)
	}
	if isFromMe {
		if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil && meta.RemoteUserID != "" {
			if !isMUC {
				senderID = meta.RemoteUserID
			}
		}
	}
	if isMUC {
		msg.Body = stripMUCSenderPrefix(msg.Body, senderID)
	}
	logJIDNormalization(nc.log, "message_sender", senderID)
	if isMUC && !fromMAM {
		if nc.mucRooms == nil || !nc.mucRooms[bareFromJid] {
			start := time.Now().Add(-defaultMAMLookback)
			nc.scheduleMUCBackfill(bareFromJid, start)
			nc.joinMUC(bareFromJid, "", "")
		}
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
			Name: &bareFromJid,
			Type: roomType,
		},
	}
	nc.log.Debug().Str("portal_id", string(portalID)).Msg("Queueing chat resync for incoming message")
	nc.bridge.QueueRemoteEvent(nc.login, resync)

	msgID := nc.extractMessageID(msg)
	if msgID == "" {
		msgID = idOverride
	}

	evt := &simplevent.Message[string]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: networkid.PortalKey{ID: portalID},
			Sender: bridgev2.EventSender{
				Sender:   MakeUserID(senderID),
				IsFromMe: isFromMe,
			},
			Timestamp: timestamp,
		},
		Data: msg.Body,
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
			if strings.HasPrefix(data, "http://") || strings.HasPrefix(data, "https://") {
				if u, err := url.Parse(data); err == nil && u.Scheme != "" && u.Host != "" {
					resp, err := http.Head(data)
					contentType := ""
					if err == nil && resp != nil && resp.StatusCode == http.StatusOK {
						contentType = resp.Header.Get("Content-Type")
					}
					if strings.HasPrefix(contentType, "image/") {
						msgType = event.MsgImage
					} else if strings.HasPrefix(contentType, "video/") {
						msgType = event.MsgVideo
					} else if strings.HasPrefix(contentType, "audio/") {
						msgType = event.MsgAudio
					} else if contentType != "" {
						msgType = event.MsgFile
					}

					if msgType != event.MsgText {
						const maxMediaSize = 50 * 1024 * 1024
						client := &http.Client{Timeout: 60 * time.Second}
						getResp, err := client.Get(data)
						if err == nil && getResp != nil {
							defer getResp.Body.Close()
							if getResp.StatusCode == http.StatusOK {
								if contentType == "" {
									contentType = getResp.Header.Get("Content-Type")
								}
								limited := io.LimitReader(getResp.Body, maxMediaSize+1)
								dataBytes, err := io.ReadAll(limited)
								if err == nil && int64(len(dataBytes)) <= maxMediaSize {
									fileName := path.Base(u.Path)
									if fileName == "" || fileName == "/" || fileName == "." {
										fileName = "file"
									}
									mediaMxc, _, err := intent.UploadMedia(ctx, portal.MXID, dataBytes, fileName, contentType)
									if err == nil {
										mediaURL = string(mediaMxc)
										body = fileName
										info = &event.FileInfo{
											MimeType: contentType,
											Size:     len(dataBytes),
										}
									}
								}
							}
						}
					}
				}
			}

			content := &event.MessageEventContent{
				MsgType: msgType,
				Body:    body,
			}
			if mediaURL != "" {
				content.URL = id.ContentURIString(mediaURL)
				content.Info = info
			}

			return &bridgev2.ConvertedMessage{
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

func (nc *MyNetworkClient) resolveMUCSenderID(msg stanza.Message, roomJID string) string {
	var mucUser MUCUserInvite
	if msg.Get(&mucUser) && mucUser.Item != nil {
		if mucUser.Item.JID != "" {
			return mucUser.Item.JID
		}
		if mucUser.Item.Nick != "" {
			return roomJID + "/" + mucUser.Item.Nick
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
	if !nc.fullSyncRequested {
		return
	}
	if nc.fullSyncRosterPending || nc.fullSyncBookmarksPending {
		return
	}
	nc.fullSyncRequested = false
}

func (nc *MyNetworkClient) WaitForConnected(ctx context.Context) bool {
	if nc.connected {
		return true
	}
	if nc.connectedCh == nil {
		return false
	}
	select {
	case <-nc.connectedCh:
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
	nc.connected = false
	if nc.xmppClient != nil {
		nc.xmppClient.Disconnect()
	}
	if nc.xmppLogFile != nil {
		if err := nc.xmppLogFile.Close(); err != nil {
			nc.log.Err(err).Msg("Failed to close XMPP traffic log file")
		}
		nc.xmppLogFile = nil
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
