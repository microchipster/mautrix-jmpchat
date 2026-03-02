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

	httpUploadServiceJID     string
	mucRooms                 map[string]bool
	mucOccupants             map[string]map[string]bool
	mucServices              map[string]bool
	rosterItems              map[string]bool
	bookmarkedRooms          map[string]bool
	fullSyncRequested        bool
	fullSyncRosterPending    bool
	fullSyncBookmarksPending bool
	fullSyncStart            time.Time
	fullSyncUseStart         bool
	connected                bool
	connectedCh              chan struct{}
	syncMu                   sync.Mutex
	syncRunning              bool
	syncStatus               string
	syncLastErr              string
	syncStartedAt            time.Time
	connectMu                sync.Mutex
	connectStarted           bool
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
	bookmarksIq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		Id:   fmt.Sprintf("bookmarks_%d", time.Now().UnixNano()),
	})
	bookmarksIq.Payload = &PrivateBookmarkQuery{Storage: &BookmarkStorage{}}
	if err := nc.xmppClient.Send(bookmarksIq); err != nil {
		return err
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
	pres.Extensions = append(pres.Extensions, stanza.MucPresence{
		Password: password,
		History: stanza.History{
			MaxChars: stanza.NewNullableInt(0),
		},
	})
	if nc.log.GetLevel() <= zerolog.DebugLevel {
		nc.log.Debug().
			Str("room_jid", roomJID).
			Str("nick", nickname).
			Bool("history_disabled", true).
			Msg("Joining MUC with history disabled to rely on MAM")
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
	if nc.mucServices == nil {
		nc.mucServices = map[string]bool{}
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
			serverDomain := strings.Split(nc.xmppClient.Session.BindJid, "@")[1]
			serverDomain = strings.Split(serverDomain, "/")[0]
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
				if nc.rosterItems != nil {
					nc.rosterItems[bareJID] = true
				}
				name := item.Name
				if name == "" {
					name = bareJID
				}
				portalID := MakePortalID(bareJID)
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
				if nc.fullSyncRequested {
					start := time.Time{}
					if nc.fullSyncUseStart {
						start = nc.fullSyncStart
					}
					if err := nc.backfillDM(context.Background(), bareJID, start); err != nil {
						nc.log.Err(err).Str("jid", bareJID).Msg("Failed to request DM MAM history")
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
			nc.log.Info().Int("items", len(payload.Conferences)).Msg("Received bookmarks")
			for _, conf := range payload.Conferences {
				roomJID := strings.TrimSpace(conf.JID)
				if roomJID == "" {
					continue
				}
				if nc.bookmarkedRooms != nil {
					nc.bookmarkedRooms[roomJID] = true
				}
				name := conf.Name
				if name == "" {
					name = roomJID
				}

				portalID := MakePortalID(roomJID)
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

				nick := strings.TrimSpace(conf.Nick)
				if nick == "" {
					nick = nc.defaultMUCNick()
				}
				nc.joinMUC(roomJID, nick, conf.Password)
				if nc.fullSyncRequested {
					start := time.Time{}
					if nc.fullSyncUseStart {
						start = nc.fullSyncStart
					}
					if err := nc.backfillMUC(context.Background(), roomJID, start); err != nil {
						nc.log.Err(err).Str("room_jid", roomJID).Msg("Failed to backfill MAM history")
					}
				} else {
					if err := nc.requestMAM(context.Background(), roomJID, 100, time.Now().Add(-defaultMAMLookback)); err != nil {
						nc.log.Err(err).Str("room_jid", roomJID).Msg("Failed to request MAM history")
					}
				}
			}
			if nc.fullSyncBookmarksPending {
				nc.fullSyncBookmarksPending = false
				nc.maybeClearFullSync()
			}
		case *PrivateBookmarkQuery:
			if payload.Storage == nil {
				nc.log.Info().Msg("Received empty bookmark storage")
				return
			}
			nc.log.Info().Int("items", len(payload.Storage.Conferences)).Msg("Received bookmarks")
			for _, conf := range payload.Storage.Conferences {
				roomJID := strings.TrimSpace(conf.JID)
				if roomJID == "" {
					continue
				}
				if nc.bookmarkedRooms != nil {
					nc.bookmarkedRooms[roomJID] = true
				}
				name := conf.Name
				if name == "" {
					name = roomJID
				}

				portalID := MakePortalID(roomJID)
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

				nick := strings.TrimSpace(conf.Nick)
				if nick == "" {
					nick = nc.defaultMUCNick()
				}
				nc.joinMUC(roomJID, nick, conf.Password)
				if nc.fullSyncRequested {
					start := time.Time{}
					if nc.fullSyncUseStart {
						start = nc.fullSyncStart
					}
					if err := nc.backfillMUC(context.Background(), roomJID, start); err != nil {
						nc.log.Err(err).Str("room_jid", roomJID).Msg("Failed to backfill MAM history")
					}
				} else {
					if err := nc.requestMAM(context.Background(), roomJID, 100, time.Now().Add(-defaultMAMLookback)); err != nil {
						nc.log.Err(err).Str("room_jid", roomJID).Msg("Failed to request MAM history")
					}
				}
			}
			if nc.fullSyncBookmarksPending {
				nc.fullSyncBookmarksPending = false
				nc.maybeClearFullSync()
			}
		case *stanza.DiscoItems:
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
			for _, identity := range payload.Identity {
				if identity.Category == "conference" && iq.From != "" {
					if nc.mucServices != nil {
						nc.mucServices[iq.From] = true
					}
					break
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

	// Ignore empty bodies unless we have OOB
	if msg.Body == "" {
		var oob stanza.OOB
		if msg.Get(&oob) && oob.URL != "" {
			msg.Body = oob.URL
		} else {
			nc.log.Debug().Str("from", fromJidStr).Str("type", string(msg.Type)).Msg("Skipping XMPP message with empty body")
			return
		}
	}

	portalID := MakePortalID(bareFromJid)

	var senderID string
	var roomType *database.RoomType
	isMUC := msg.Type == stanza.MessageTypeGroupchat

	if isMUC {
		senderID = nc.resolveMUCSenderID(msg, bareFromJid)
		if senderID == "" {
			// For MUCs, include the room and occupant nickname for uniqueness
			senderID = fromJidStr
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
	if isMUC && !fromMAM {
		if nc.mucRooms == nil || !nc.mucRooms[bareFromJid] {
			nc.joinMUC(bareFromJid, "", "")
			go func(roomJID string) {
				if err := nc.requestMAM(context.Background(), roomJID, 100, time.Now().Add(-defaultMAMLookback)); err != nil {
					nc.log.Err(err).Str("room_jid", roomJID).Msg("Failed to request MAM history")
				}
			}(bareFromJid)
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
		return
	}
	if err := nc.requestBookmarks(); err != nil {
		nc.setSyncError(err)
		if notify != nil {
			notify(fmt.Sprintf("Sync failed requesting bookmarks: %s", err.Error()))
		}
		return
	}

	update("Backfill in progress (MAM)")
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		if !nc.fullSyncRequested {
			update("Sync complete")
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
			return
		}
	}

	nc.fullSyncRequested = false
	update("Sync timed out (backfill may still be running)")
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
