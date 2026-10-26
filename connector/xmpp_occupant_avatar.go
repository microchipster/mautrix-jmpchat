// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const (
	// mucOccupantAvatarMinGap paces occupant avatar fetches (4/s) so joining a
	// large room does not burst the server with IQs.
	mucOccupantAvatarMinGap  = 250 * time.Millisecond
	mucOccupantAvatarTimeout = 20 * time.Second
	// mucOccupantAvatarRoomErrMax is how many permanent avatar fetch failures a
	// single room may produce before probing is paused for that room.
	mucOccupantAvatarRoomErrMax = 5
	// mucOccupantAvatarRoomBlockTTL is how long a room stays on the refusal
	// list before avatar probing is retried.
	mucOccupantAvatarRoomBlockTTL = time.Hour
)

// occupantAvatarRoomBlocked reports whether the room is currently on the
// avatar refusal list. Expired entries are cleared on read.
func (nc *MyNetworkClient) occupantAvatarRoomBlocked(roomJID string) bool {
	nc.occupantAvatarMu.Lock()
	defer nc.occupantAvatarMu.Unlock()
	until, ok := nc.occupantAvatarBlocks[roomJID]
	if !ok {
		return false
	}
	if time.Now().Before(until) {
		return true
	}
	delete(nc.occupantAvatarBlocks, roomJID)
	return false
}

// noteOccupantAvatarRoomError records a permanent avatar fetch failure for the
// room. After mucOccupantAvatarRoomErrMax failures the room is blocked to
// avoid hammering a server that refuses the avatar nodes.
func (nc *MyNetworkClient) noteOccupantAvatarRoomError(roomJID string) {
	nc.occupantAvatarMu.Lock()
	defer nc.occupantAvatarMu.Unlock()
	if nc.occupantAvatarRoomErrs == nil {
		nc.occupantAvatarRoomErrs = map[string]int{}
	}
	nc.occupantAvatarRoomErrs[roomJID]++
	if nc.occupantAvatarRoomErrs[roomJID] >= mucOccupantAvatarRoomErrMax {
		if nc.occupantAvatarBlocks == nil {
			nc.occupantAvatarBlocks = map[string]time.Time{}
		}
		nc.occupantAvatarBlocks[roomJID] = time.Now().Add(mucOccupantAvatarRoomBlockTTL)
		delete(nc.occupantAvatarRoomErrs, roomJID)
		nc.log.Warn().Str("room_jid", roomJID).Msg("Pausing MUC occupant avatar probing after repeated failures")
	}
}

// clearOccupantAvatarRoomError resets the failure counter after a successful
// avatar fetch from the room.
func (nc *MyNetworkClient) clearOccupantAvatarRoomError(roomJID string) {
	nc.occupantAvatarMu.Lock()
	defer nc.occupantAvatarMu.Unlock()
	delete(nc.occupantAvatarRoomErrs, roomJID)
	delete(nc.occupantAvatarBlocks, roomJID)
}

// maybeFetchOccupantAvatar triggers a XEP-0084 avatar fetch for a MUC occupant
// whose real JID is unknown. Anonymous rooms only expose the room-scoped
// resource (room@conference.example.com/nick), so the contact-rule vCard flow
// cannot be used; the avatar nodes are probed via the occupant's full JID
// instead (matching the reference bridge behaviour).
func (nc *MyNetworkClient) maybeFetchOccupantAvatar(roomJID, resource string, pres stanza.Presence) {
	if nc.bridge == nil || nc.login == nil {
		return
	}
	roomJID = strings.TrimSpace(roomJID)
	resource = strings.TrimSpace(resource)
	if roomJID == "" || resource == "" {
		return
	}
	if isCheogramGroupRoomJID(roomJID) {
		// Phone numbers do not have meaningful XEP-0084 avatars.
		return
	}
	if strings.HasPrefix(resource, "occ-") {
		// Occupant-id pseudo resources are not addressable client JIDs.
		return
	}
	if nc.occupantAvatarRoomBlocked(roomJID) {
		return
	}
	var update XMPPVCardUpdate
	if !pres.Get(&update) {
		return
	}
	hash := strings.TrimSpace(update.Photo)
	if hash == "" {
		return
	}
	go nc.refreshOccupantXEP0084Avatar(roomJID, resource, hash)
}

// refreshOccupantXEP0084Avatar fetches the occupant's XEP-0084 avatar and
// stores it on the occupant ghost (room@conference/nick).
func (nc *MyNetworkClient) refreshOccupantXEP0084Avatar(roomJID, resource, hash string) {
	if !nc.isXMPPConnected() {
		return
	}
	fullJID := roomJID + "/" + resource
	userID := nc.senderUserID(fullJID)
	if userID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), mucOccupantAvatarTimeout)
	defer cancel()
	if !nc.awaitOccupantAvatarSlot(ctx) {
		return
	}
	ghost, err := nc.bridge.GetGhostByID(ctx, userID)
	if err != nil {
		nc.log.Debug().Err(err).Str("jid", fullJID).Msg("Failed to load ghost for occupant avatar")
		return
	}
	meta, _ := ghost.Metadata.(*GhostMetadata)
	if meta == nil {
		meta = &GhostMetadata{}
	}
	if meta.AvatarHash == hash && ghost.AvatarMXC != "" {
		return
	}
	if meta.AvatarFetchErrorHash == hash {
		return
	}
	data, avatarID, mime, err := nc.fetchOccupantXEP0084Avatar(ctx, fullJID)
	if err != nil {
		if isPermanentVCardError(err) {
			nc.noteOccupantAvatarRoomError(roomJID)
			if meta.AvatarFetchErrorHash != hash {
				meta.AvatarFetchErrorHash = hash
				ghost.Metadata = meta
				if dbErr := nc.bridge.DB.Ghost.Update(ctx, ghost.Ghost); dbErr != nil {
					nc.log.Warn().Err(dbErr).Str("jid", fullJID).Msg("Failed to save stamped occupant avatar fetch error")
				}
			}
			return
		}
		nc.log.Debug().Err(err).Str("jid", fullJID).Msg("Failed to fetch occupant XEP-0084 avatar")
		return
	}
	if len(data) == 0 || len(data) > maxVCardAvatarSize {
		return
	}
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if !allowedVCardAvatarMime(mime) {
		return
	}
	if avatarID == "" {
		avatarID = hash
	}
	ghost.UpdateInfo(ctx, &bridgev2.UserInfo{Avatar: &bridgev2.Avatar{
		ID: networkid.AvatarID("xep0084:" + avatarID),
		Get: func(context.Context) ([]byte, error) {
			return data, nil
		},
	}})
	now := time.Now()
	meta.AvatarHash = hash
	meta.AvatarFetchedAt = &now
	meta.AvatarFetchErrorHash = ""
	ghost.Metadata = meta
	if err := nc.bridge.DB.Ghost.Update(ctx, ghost.Ghost); err != nil {
		nc.log.Warn().Err(err).Str("jid", fullJID).Msg("Failed to save occupant avatar metadata")
		return
	}
	nc.log.Info().Str("room_jid", roomJID).Str("resource", resource).Msg("Updated MUC occupant avatar")
	nc.clearOccupantAvatarRoomError(roomJID)
}

// awaitOccupantAvatarSlot waits until this login may issue the next occupant
// avatar request. It reports false when the context is cancelled first.
func (nc *MyNetworkClient) awaitOccupantAvatarSlot(ctx context.Context) bool {
	nc.occupantAvatarMu.Lock()
	now := time.Now()
	if !now.Before(nc.occupantAvatarNext) {
		nc.occupantAvatarNext = now.Add(mucOccupantAvatarMinGap)
		nc.occupantAvatarMu.Unlock()
		return true
	}
	wait := time.Until(nc.occupantAvatarNext)
	nc.occupantAvatarMu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	}
	nc.occupantAvatarMu.Lock()
	nc.occupantAvatarNext = time.Now().Add(mucOccupantAvatarMinGap)
	nc.occupantAvatarMu.Unlock()
	return true
}

// fetchOccupantXEP0084Avatar queries the metadata and data nodes through the
// occupant's full JID and returns the decoded image, its XEP-0084 id and mime.
func (nc *MyNetworkClient) fetchOccupantXEP0084Avatar(ctx context.Context, fullJID string) ([]byte, string, string, error) {
	if nc.xmppClient == nil {
		return nil, "", "", fmt.Errorf("not connected to XMPP")
	}
	metaIQ, err := stanza.NewIQ(stanza.Attrs{Type: stanza.IQTypeGet, To: fullJID, Id: fmt.Sprintf("occupant_avatar_meta_%d", time.Now().UnixNano())})
	if err != nil {
		return nil, "", "", err
	}
	metaIQ.Payload = &stanza.PubSubGeneric{Items: &stanza.Items{Node: NSAvatarMetadata}}
	ch, err := nc.xmppClient.SendIQ(ctx, metaIQ)
	if err != nil {
		return nil, "", "", err
	}
	avatarID, mime := "", ""
	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			return nil, "", "", permanentVCardError{fmt.Errorf("avatar metadata request returned %s", resp.Type)}
		}
		items, ok := resp.Payload.(*stanza.PubSubGeneric)
		if !ok || items.Items == nil || len(items.Items.List) == 0 {
			return nil, "", "", fmt.Errorf("avatar metadata node is empty")
		}
		found := false
		for _, item := range items.Items.List {
			if item.Any == nil {
				continue
			}
			raw, marshalErr := xml.Marshal(item.Any)
			if marshalErr != nil {
				continue
			}
			if meta, ok := parseAvatarMetadata(raw); ok {
				info := meta.Info[0]
				avatarID = strings.TrimSpace(info.ID)
				mime = strings.TrimSpace(info.Type)
				found = true
				break
			}
		}
		if !found {
			return nil, "", "", fmt.Errorf("avatar metadata node is empty")
		}
	case <-ctx.Done():
		return nil, "", "", ctx.Err()
	}
	dataIQ, err := stanza.NewIQ(stanza.Attrs{Type: stanza.IQTypeGet, To: fullJID, Id: fmt.Sprintf("occupant_avatar_data_%d", time.Now().UnixNano())})
	if err != nil {
		return nil, "", "", err
	}
	dataIQ.Payload = &stanza.PubSubGeneric{Items: &stanza.Items{Node: NSAvatarData}}
	ch, err = nc.xmppClient.SendIQ(ctx, dataIQ)
	if err != nil {
		return nil, "", "", err
	}
	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			return nil, "", "", permanentVCardError{fmt.Errorf("avatar data request returned %s", resp.Type)}
		}
		items, ok := resp.Payload.(*stanza.PubSubGeneric)
		if !ok || items.Items == nil {
			return nil, "", "", fmt.Errorf("avatar data node is empty")
		}
		for _, item := range items.Items.List {
			if item.Any == nil {
				continue
			}
			raw, marshalErr := xml.Marshal(item.Any)
			if marshalErr != nil {
				continue
			}
			if data, ok := decodeAvatarData(raw); ok {
				return data, avatarID, mime, nil
			}
		}
		return nil, "", "", fmt.Errorf("avatar data node is empty")
	case <-ctx.Done():
		return nil, "", "", ctx.Err()
	}
}

// decodeAvatarData decodes a XEP-0084 avatar data element into raw bytes.
// Whitespace inside the base64 payload is ignored.
func decodeAvatarData(raw []byte) ([]byte, bool) {
	var parsed avatarData
	if err := xml.Unmarshal(raw, &parsed); err != nil {
		return nil, false
	}
	encoded := stripASCIIWhitespace(strings.TrimSpace(parsed.Data))
	if encoded == "" {
		return nil, false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 {
		return nil, false
	}
	return decoded, true
}
