// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"time"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
)

const (
	NSMUC       = "http://jabber.org/protocol/muc"
	NSMUCOwner  = "http://jabber.org/protocol/muc#owner"
	NSMUCConfig = "http://jabber.org/protocol/muc#roomconfig"
)

type MUCOwnerQuery struct {
	XMLName xml.Name `xml:"http://jabber.org/protocol/muc#owner query"`
	Form    XData    `xml:"x"`
}

func (q *MUCOwnerQuery) Namespace() string {
	return q.XMLName.Space
}

func (q *MUCOwnerQuery) GetSet() *stanza.ResultSet {
	return nil
}

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTIQ, xml.Name{Space: NSMUCOwner, Local: "query"}, MUCOwnerQuery{})
}

// initiateGroupChat normalizes the participants and, for real MUC rooms, queues
// the pending MUC creation, joins the group MUC and publishes an autojoin
// bookmark. Cheogram gateway group JIDs are not MUCs: they skip all of that and
// only resolve the target JID/name (the caller still creates the portal).
// It returns the resolved room JID and room name.
func (nc *MyNetworkClient) initiateGroupChat(ctx context.Context, name string, participants []string) (string, string, error) {
	roomJID, err := normalizedCheogramGroupJID(participants)
	if err != nil {
		return "", "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = groupRoomNameFromParticipants(ctx, nc.bridge, participants, "", nc.contactNameSuffix())
	}

	if !isCheogramGatewayJID(roomJID) {
		nc.queuePendingMUCCreation(roomJID, pendingMUCCreation{
			name:         name,
			participants: append([]string(nil), participants...),
		})
		nc.joinMUC(roomJID, "", "")

		if nc.bookmarkedRooms != nil {
			nc.bookmarkedRooms[roomJID] = true
		}
		if name != "" {
			if nc.bookmarkedRoomNames == nil {
				nc.bookmarkedRoomNames = map[string]string{}
			}
			nc.bookmarkedRoomNames[roomJID] = name
		}
		if err := nc.publishBookmark2Merged(ctx, BookmarkConference{
			JID:      roomJID,
			Name:     name,
			Autojoin: "true",
			Nick:     nc.defaultMUCNick(),
		}, true); err != nil {
			nc.log.Err(err).Str("room_jid", roomJID).Msg("Failed to publish group chat bookmark")
		}
	} else {
		nc.log.Debug().
			Str("room_jid", roomJID).
			Msg("Skipping MUC creation, join and bookmark for Cheogram gateway group JID")
	}

	return roomJID, name, nil
}

func (nc *MyNetworkClient) createGroupChat(ctx context.Context, name string, participants []string) error {
	roomJID, _, err := nc.initiateGroupChat(ctx, name, participants)
	if err != nil {
		return err
	}
	portalID := MakePortalID(roomJID)
	resync := &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    networkid.PortalKey{ID: portalID},
			CreatePortal: true,
			Timestamp:    time.Now(),
		},
		ChatInfo: nil,
	}
	nc.bridge.QueueRemoteEvent(nc.login, resync)
	return nil
}

func normalizedCheogramGroupJID(participants []string) (string, error) {
	numbers := make([]string, 0, len(participants))
	seen := make(map[string]struct{}, len(participants))
	for _, participant := range participants {
		jid := CanonicalBareJID(participant)
		if jid == "" {
			continue
		}
		if !strings.HasSuffix(jid, "@cheogram.com") {
			return "", fmt.Errorf("participant %s is not a cheogram.com address", participant)
		}
		localpart := strings.TrimSuffix(jid, "@cheogram.com")
		if localpart == "" || strings.Contains(localpart, ",") {
			return "", fmt.Errorf("participant %s is not a direct phone/JID", participant)
		}
		if _, ok := seen[localpart]; ok {
			continue
		}
		seen[localpart] = struct{}{}
		numbers = append(numbers, localpart)
	}
	if len(numbers) < 2 {
		return "", fmt.Errorf("group chat requires at least two unique cheogram.com participants")
	}
	sort.Strings(numbers)
	return strings.Join(numbers, ",") + "@cheogram.com", nil
}

func (nc *MyNetworkClient) getMUCService(ctx context.Context) (string, error) {
	for jid := range nc.mucServices {
		return jid, nil
	}
	server := nc.serverDomain()
	if server == "" {
		return "", fmt.Errorf("unable to determine server domain")
	}
	items, err := nc.requestDiscoItems(ctx, server)
	if err != nil {
		return "", err
	}
	for _, item := range items.Items {
		if item.JID == "" {
			continue
		}
		info, err := nc.requestDiscoInfo(ctx, item.JID)
		if err != nil {
			continue
		}
		if isMUCService(info) {
			if nc.mucServices != nil {
				nc.mucServices[item.JID] = true
			}
			return item.JID, nil
		}
	}
	return "", fmt.Errorf("no MUC service discovered")
}

func isMUCService(info *stanza.DiscoInfo) bool {
	if info == nil {
		return false
	}
	for _, identity := range info.Identity {
		if identity.Category == "conference" {
			return true
		}
	}
	for _, feature := range info.Features {
		if feature.Var == NSMUC {
			return true
		}
	}
	return false
}

func (nc *MyNetworkClient) configureMUC(ctx context.Context, roomJID string, name string) error {
	if !nc.isXMPPConnected() {
		return fmt.Errorf("XMPP client not connected")
	}
	fields := []XDataField{
		{Var: "FORM_TYPE", Type: "hidden", Values: []string{NSMUCConfig}},
		{Var: "muc#roomconfig_roomname", Values: []string{name}},
		{Var: "muc#roomconfig_membersonly", Values: []string{"1"}},
		{Var: "muc#roomconfig_persistentroom", Values: []string{"1"}},
		{Var: "muc#roomconfig_allowinvites", Values: []string{"1"}},
		{Var: "muc#roomconfig_publicroom", Values: []string{"0"}},
	}
	q := &MUCOwnerQuery{
		Form: XData{Type: "submit", Fields: fields},
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	iq, _ := stanza.NewIQ(stanza.Attrs{Type: stanza.IQTypeSet, To: roomJID, Id: fmt.Sprintf("muc_config_%d", time.Now().UnixNano())})
	iq.Payload = q
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			return fmt.Errorf("muc config failed: %s", resp.Type)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (nc *MyNetworkClient) inviteToMUC(roomJID string, jid string, reason string) error {
	if !nc.isXMPPConnected() {
		return fmt.Errorf("XMPP client not connected")
	}
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return fmt.Errorf("jid is empty")
	}
	msg := stanza.NewMessage(stanza.Attrs{To: roomJID})
	msg.Extensions = append(msg.Extensions, MUCUserInvite{
		Invite: &MUCInvite{
			To:     jid,
			Reason: reason,
		},
	})
	return nc.sendXMPP(msg)
}

// extractMUCSubject returns a non-empty room subject from a stanza.
func extractMUCSubject(msg stanza.Message) (string, bool) {
	subject := strings.TrimSpace(stanza.StringValue(msg.Subject))
	if subject == "" {
		return "", false
	}
	return subject, true
}

// handleIncomingMUCSubject turns a body-less groupchat subject change into a
// Matrix room topic update instead of bridging it as an empty message.
func (nc *MyNetworkClient) handleIncomingMUCSubject(msg stanza.Message, bareFromJid string) bool {
	if strings.TrimSpace(stanza.StringValue(msg.Body)) != "" {
		return false
	}
	subject, ok := extractMUCSubject(msg)
	if !ok {
		return false
	}
	nc.log.Info().Str("room_jid", bareFromJid).Str("subject", subject).Msg("Received MUC subject update")
	nc.bridge.QueueRemoteEvent(nc.login, &simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: networkid.PortalKey{ID: MakePortalID(bareFromJid)},
			Timestamp: time.Now(),
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{Topic: &subject},
		},
	})
	return true
}
