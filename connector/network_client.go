package connector

import (
	"context"
	"encoding/xml"
	"fmt"
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

// Ensure MyNetworkClient implements NetworkAPI.
var _ bridgev2.NetworkAPI = (*MyNetworkClient)(nil)

// MyNetworkClient implements the bridgev2.NetworkAPI for interacting
// with the simple network on behalf of a specific user login.
type MyNetworkClient struct {
	log       zerolog.Logger
	bridge    *bridgev2.Bridge
	login     *bridgev2.UserLogin
	connector *MyConnector

	xmppClient *xmpp.Client
	xmppRouter *xmpp.Router
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
	nc.bridge.Log.Info().Str("jid", config.Jid).Msg("Connecting to XMPP...")

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
		}
	}()
}

func (nc *MyNetworkClient) handleXMPPPresence(s xmpp.Sender, p stanza.Packet) {
	pres, ok := p.(stanza.Presence)
	if !ok {
		return
	}
	nc.log.Info().
		Str("from", pres.From).
		Str("type", string(pres.Type)).
		Msg("Received XMPP presence")
}

func (nc *MyNetworkClient) handleXMPPIq(s xmpp.Sender, p stanza.Packet) {
	iq, ok := p.(*stanza.IQ)
	if !ok {
		return
	}
	nc.log.Info().
		Str("from", iq.From).
		Str("type", string(iq.Type)).
		Str("id", iq.Id).
		Msg("Received XMPP IQ")
}

func (nc *MyNetworkClient) handleXMPPMessage(s xmpp.Sender, p stanza.Packet) {
	msg, ok := p.(stanza.Message)
	if !ok {
		return
	}

	// XMPP JIDs look like user@domain.com/resource
	fromJidStr := msg.From
	fromParts := strings.Split(fromJidStr, "/")
	bareFromJid := fromParts[0]

	// Ignore empty bodies
	if msg.Body == "" {
		return
	}

	nc.log.Info().
		Str("from", fromJidStr).
		Str("type", string(msg.Type)).
		Str("body", msg.Body).
		Msg("Received XMPP message")

	portalID := networkid.PortalID(bareFromJid)

	var senderID string
	var roomType *database.RoomType
	isMUC := msg.Type == stanza.MessageTypeGroupchat

	if isMUC {
		// For MUCs, the sender is identified by the resource
		if len(fromParts) > 1 {
			senderID = fromParts[1]
		} else {
			senderID = bareFromJid
		}
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
	nc.bridge.QueueRemoteEvent(nc.login, resync)

	evt := &simplevent.Message[string]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: networkid.PortalKey{ID: portalID},
			Sender: bridgev2.EventSender{
				Sender:   networkid.UserID(senderID),
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

			return &bridgev2.ConvertedMessage{
				Parts: []*bridgev2.ConvertedMessagePart{{
					Type: event.EventMessage,
					Content: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    data,
					},
				}},
			}, nil
		},
	}

	// Ensure that if it has no ID, we generate one
	if evt.ID == "" {
		evt.ID = networkid.MessageID(fmt.Sprintf("remote-%s-%d", portalID, time.Now().UnixNano()))
	}

	nc.bridge.QueueRemoteEvent(nc.login, evt)
}

// Disconnect cleans up the XMPP connection.
func (nc *MyNetworkClient) Disconnect() {
	nc.log.Info().Msg("MyNetworkClient Disconnect called")
	if nc.xmppClient != nil {
		nc.xmppClient.Disconnect()
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
	bareUserID := strings.Split(string(userID), "/")[0]
	return bareUserID == meta.RemoteUserID
}

// IsLoggedIn checks if we have a valid access token.
func (nc *MyNetworkClient) IsLoggedIn() bool {
	meta, ok := nc.login.Metadata.(*LoginMetadata)
	return ok && meta != nil && meta.AccessToken != ""
}
