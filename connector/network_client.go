package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
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

func (nc *MyNetworkClient) requestBookmarks() error {
	if nc.xmppClient == nil {
		return fmt.Errorf("XMPP client not connected")
	}
	bookmarksIq, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		Id:   fmt.Sprintf("bookmarks_%d", time.Now().UnixNano()),
	})
	bookmarksIq.Payload = &BookmarkStorage{}
	if err := nc.xmppClient.Send(bookmarksIq); err != nil {
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
	})

	if err := nc.xmppClient.Send(pres); err != nil {
		nc.log.Err(err).Str("room_jid", roomJID).Str("nick", nickname).Msg("Failed to join MUC")
		return
	}

	nc.log.Info().Str("room_jid", roomJID).Str("nick", nickname).Msg("Sent MUC join presence")
}

// Connect is called to establish the connection to XMPP.
func (nc *MyNetworkClient) Connect(ctx context.Context) {
	nc.log.Info().Msg("MyNetworkClient Connect called")

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

	// Ignore empty bodies
	if msg.Body == "" {
		nc.log.Debug().Str("from", fromJidStr).Str("type", string(msg.Type)).Msg("Skipping XMPP message with empty body")
		return
	}

	portalID := MakePortalID(bareFromJid)

	var senderID string
	var roomType *database.RoomType
	isMUC := msg.Type == stanza.MessageTypeGroupchat

	if isMUC {
		// For MUCs, include the room and occupant nickname for uniqueness
		senderID = fromJidStr
		roomType = ptr.Ptr(database.RoomTypeDefault)
	} else {
		// DMs
		senderID = bareFromJid
		roomType = ptr.Ptr(database.RoomTypeDM)
	}

	// First send a ChatResync to create or update the portal with correct info
	resync := &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    networkid.PortalKey{ID: portalID},
			CreatePortal: true,
			Timestamp:    time.Now(),
		},
		ChatInfo: &bridgev2.ChatInfo{
			Name: &bareFromJid,
			Type: roomType,
		},
	}
	nc.log.Debug().Str("portal_id", string(portalID)).Msg("Queueing chat resync for incoming message")
	nc.bridge.QueueRemoteEvent(nc.login, resync)

	evt := &simplevent.Message[string]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: networkid.PortalKey{ID: portalID},
			Sender: bridgev2.EventSender{
				Sender:   MakeUserID(senderID),
				IsFromMe: false,
			},
			Timestamp: time.Now(),
		},
		Data: msg.Body,
		ID:   networkid.MessageID(msg.Id),
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data string) (*bridgev2.ConvertedMessage, error) {
			// Save the room type metadata inside the ConvertMessageFunc as we have the portal
			meta, _ := portal.Metadata.(*PortalMetadata)
			if meta != nil && meta.IsMUC != isMUC {
				meta.IsMUC = isMUC
				portal.Save(ctx)
			}

			msgType := event.MsgText
			body := data
			if strings.HasPrefix(data, "http://") || strings.HasPrefix(data, "https://") {
				if u, err := url.Parse(data); err == nil && u.Scheme != "" && u.Host != "" {
					resp, err := http.Head(data)
					if err == nil && resp.StatusCode == http.StatusOK {
						contentType := resp.Header.Get("Content-Type")
						if strings.HasPrefix(contentType, "image/") {
							msgType = event.MsgImage
						} else if strings.HasPrefix(contentType, "video/") {
							msgType = event.MsgVideo
						} else if strings.HasPrefix(contentType, "audio/") {
							msgType = event.MsgAudio
						} else {
							msgType = event.MsgFile
						}
					}
				}
			}

			return &bridgev2.ConvertedMessage{
				Parts: []*bridgev2.ConvertedMessagePart{{
					Type: event.EventMessage,
					Content: &event.MessageEventContent{
						MsgType: msgType,
						Body:    body,
					},
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

// Disconnect cleans up the XMPP connection.
func (nc *MyNetworkClient) Disconnect() {
	nc.log.Info().Msg("MyNetworkClient Disconnect called")
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
