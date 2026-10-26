// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const (
	// NSVCardTemp is the vcard-temp namespace (XEP-0054).
	NSVCardTemp = "vcard-temp"
	// NSVCardUpdate is the presence-based vCard update namespace (XEP-0153).
	NSVCardUpdate = "vcard-temp:x:update"

	maxVCardAvatarSize         = 1 << 20 // 1 MiB
	vcardFetchTimeout          = 30 * time.Second
	vcardAvatarRefreshInterval = 24 * time.Hour
	vcardAvatarRefreshInitial  = time.Minute
	vcardAvatarRefreshStagger  = 500 * time.Millisecond
)

// permanentVCardError marks vCard fetch failures that will not succeed on
// retry with the same photo hash (for example a stanza error response).
type permanentVCardError struct {
	err error
}

func (p permanentVCardError) Error() string {
	return p.err.Error()
}

func (p permanentVCardError) Unwrap() error {
	return p.err
}

// isPermanentVCardError reports whether a vCard fetch error should be stamped.
func isPermanentVCardError(err error) bool {
	var permanent permanentVCardError
	return errors.As(err, &permanent)
}

// vcardAvatarNeedsRefresh reports whether the stored avatar should be fetched
// again because the daily refresh interval elapsed (or it was never fetched).
func vcardAvatarNeedsRefresh(meta *GhostMetadata, now time.Time) bool {
	if meta == nil || meta.AvatarHash == "" {
		return false
	}
	if meta.AvatarFetchedAt == nil {
		return true
	}
	return now.Sub(*meta.AvatarFetchedAt) >= vcardAvatarRefreshInterval
}

// XMPPVCardUpdate is the vcard-temp:x:update presence extension carrying the
// SHA1 hash of the user's vCard photo.
type XMPPVCardUpdate struct {
	stanza.PresExtension
	XMLName xml.Name `xml:"vcard-temp:x:update x"`
	Photo   string   `xml:"photo"`
}

// vcardTemp is a minimal vcard-temp payload, only decoding PHOTO.
type vcardTemp struct {
	XMLName xml.Name        `xml:"vcard-temp vCard"`
	Photo   *vcardTempPhoto `xml:"PHOTO"`
}

type vcardTempPhoto struct {
	Binval string `xml:"BINVAL"`
	Type   string `xml:"TYPE"`
}

// Namespace lets vcardTemp implement the IQPayload interface.
func (v *vcardTemp) Namespace() string {
	return v.XMLName.Space
}

// GetSet lets vcardTemp implement the IQPayload interface.
func (v *vcardTemp) GetSet() *stanza.ResultSet {
	return nil
}

func init() {
	stanza.TypeRegistry.MapExtension(stanza.PKTPresence, xml.Name{Space: NSVCardUpdate, Local: "x"}, XMPPVCardUpdate{})
	stanza.TypeRegistry.MapExtension(stanza.PKTIQ, xml.Name{Space: NSVCardTemp, Local: "vCard"}, vcardTemp{})
}

// handleVCardUpdatePresence reacts to XEP-0153 photo hash announcements by
// fetching the contact's vCard photo in the background.
func (nc *MyNetworkClient) handleVCardUpdatePresence(bareFromJID string, pres stanza.Presence) {
	if nc.bridge == nil || nc.login == nil || bareFromJID == "" {
		return
	}
	lower := strings.ToLower(bareFromJID)
	if strings.HasSuffix(lower, "@cheogram.com") {
		// Phone numbers do not have meaningful vCard avatars.
		return
	}
	if nc.mucRooms != nil && nc.mucRooms[bareFromJID] {
		return
	}
	if isCheogramGroupRoomJID(bareFromJID) {
		return
	}
	if own := nc.ownBareJID(); own != "" && strings.EqualFold(bareFromJID, own) {
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
	go nc.refreshVCardAvatar(bareFromJID, hash)
}

// refreshVCardAvatar fetches a vCard photo and updates the ghost avatar when it
// is new or the announced hash changed.
func (nc *MyNetworkClient) refreshVCardAvatar(jid, hash string) {
	ctx, cancel := context.WithTimeout(context.Background(), vcardFetchTimeout)
	defer cancel()
	nc.refreshVCardAvatarContext(ctx, jid, hash, false)
}

// refreshVCardAvatarContext fetches a vCard photo. When force is set the
// same-hash dedupe is bypassed so the daily refresh can pick up changes that
// were never announced via presence.
func (nc *MyNetworkClient) refreshVCardAvatarContext(ctx context.Context, jid, hash string, force bool) {
	if !nc.isXMPPConnected() {
		return
	}
	ghost, err := nc.bridge.GetGhostByID(ctx, MakeUserID(jid))
	if err != nil {
		nc.log.Debug().Err(err).Str("jid", jid).Msg("Failed to load ghost for vCard avatar")
		return
	}
	meta, _ := ghost.Metadata.(*GhostMetadata)
	if meta == nil {
		meta = &GhostMetadata{}
	}
	if !force && meta.AvatarHash == hash && ghost.AvatarMXC != "" {
		return
	}
	if meta.AvatarFetchErrorHash == hash {
		return
	}
	photo, _, err := nc.fetchVCardPhoto(ctx, jid)
	if err != nil {
		if isPermanentVCardError(err) {
			if meta.AvatarFetchErrorHash != hash {
				meta.AvatarFetchErrorHash = hash
				ghost.Metadata = meta
				if dbErr := nc.bridge.DB.Ghost.Update(ctx, ghost.Ghost); dbErr != nil {
					nc.log.Warn().Err(dbErr).Str("jid", jid).Msg("Failed to save stamped vCard fetch error")
				}
			}
			nc.log.Debug().Err(err).Str("jid", jid).Msg("Stamped permanent vCard fetch error")
			return
		}
		nc.log.Debug().Err(err).Str("jid", jid).Msg("Failed to fetch vCard avatar")
		return
	}
	if len(photo) == 0 || len(photo) > maxVCardAvatarSize {
		return
	}
	if !allowedVCardAvatarMime(http.DetectContentType(photo)) {
		return
	}
	ghost.UpdateInfo(ctx, &bridgev2.UserInfo{Avatar: &bridgev2.Avatar{
		ID: networkid.AvatarID("vcard:" + hash),
		Get: func(context.Context) ([]byte, error) {
			return photo, nil
		},
	}})
	now := time.Now()
	meta.AvatarHash = hash
	meta.AvatarFetchedAt = &now
	meta.AvatarFetchErrorHash = ""
	ghost.Metadata = meta
	if err := nc.bridge.DB.Ghost.Update(ctx, ghost.Ghost); err != nil {
		nc.log.Warn().Err(err).Str("jid", jid).Msg("Failed to save vCard avatar metadata")
		return
	}
	nc.log.Info().Str("jid", jid).Msg("Updated XMPP contact avatar")
}

// startVCardAvatarRefreshLoop starts the daily avatar refresh worker once per
// login, so avatars of contacts that never re-announce their hash stay fresh.
func (nc *MyNetworkClient) startVCardAvatarRefreshLoop() {
	nc.vcardRefreshOnce.Do(func() {
		go func() {
			time.Sleep(vcardAvatarRefreshInitial)
			for {
				nc.refreshStaleVCardAvatars()
				time.Sleep(vcardAvatarRefreshInterval)
			}
		}()
	})
}

// refreshStaleVCardAvatars re-fetches roster contacts whose avatar was not
// fetched within the refresh interval.
func (nc *MyNetworkClient) refreshStaleVCardAvatars() {
	if nc.bridge == nil || nc.login == nil || !nc.isXMPPConnected() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	now := time.Now()
	for _, jid := range nc.rosterContactList() {
		if ctx.Err() != nil {
			return
		}
		if strings.HasSuffix(strings.ToLower(jid), "@cheogram.com") {
			continue
		}
		ghost, err := nc.bridge.GetGhostByID(ctx, MakeUserID(jid))
		if err != nil {
			continue
		}
		meta, _ := ghost.Metadata.(*GhostMetadata)
		if !vcardAvatarNeedsRefresh(meta, now) {
			continue
		}
		nc.refreshVCardAvatarContext(ctx, jid, meta.AvatarHash, true)
		select {
		case <-ctx.Done():
			return
		case <-time.After(vcardAvatarRefreshStagger):
		}
	}
}

// fetchVCardPhoto requests the vCard of a contact and decodes its PHOTO into
// raw bytes. Missing photos are not an error and yield nil data.
func (nc *MyNetworkClient) fetchVCardPhoto(ctx context.Context, jid string) ([]byte, string, error) {
	if nc.xmppClient == nil {
		return nil, "", fmt.Errorf("not connected to XMPP")
	}
	iq, err := stanza.NewIQ(stanza.Attrs{Type: stanza.IQTypeGet, To: jid, Id: fmt.Sprintf("vcard_get_%d", time.Now().UnixNano())})
	if err != nil {
		return nil, "", err
	}
	iq.Payload = &vcardTemp{XMLName: xml.Name{Space: NSVCardTemp, Local: "vCard"}}
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return nil, "", err
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, "", fmt.Errorf("vCard response channel closed")
		}
		if resp.Type != stanza.IQTypeResult {
			return nil, "", permanentVCardError{fmt.Errorf("vCard request failed: %s", resp.Type)}
		}
		vcard, ok := resp.Payload.(*vcardTemp)
		if !ok || vcard.Photo == nil {
			return nil, "", nil
		}
		binval := strings.Join(strings.Fields(vcard.Photo.Binval), "")
		if binval == "" {
			return nil, "", nil
		}
		data, err := base64.StdEncoding.DecodeString(binval)
		if err != nil {
			return nil, "", fmt.Errorf("failed to decode vCard photo: %w", err)
		}
		return data, vcard.Photo.Type, nil
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
}

// allowedVCardAvatarMime reports whether a detected content type is one of the
// image formats accepted for XMPP avatars.
func allowedVCardAvatarMime(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
