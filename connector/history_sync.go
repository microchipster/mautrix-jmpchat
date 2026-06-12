package connector

import (
	"context"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const (
	automaticCatchupLookback = 5 * 24 * time.Hour
	historySyncOverlap       = time.Second
)

func shouldUpdateHistoryProgress(existingTS *time.Time, existingID string, newTS time.Time, newID string) bool {
	if newTS.IsZero() {
		return false
	}
	if existingTS == nil || existingTS.IsZero() {
		return true
	}
	if newTS.After(*existingTS) {
		return true
	}
	if newTS.Equal(*existingTS) && newID != "" && existingID == "" {
		return true
	}
	return false
}

func (nc *MyNetworkClient) markPortalHistoryProgress(ctx context.Context, portal *bridgev2.Portal, timestamp time.Time, messageID string, serverMessageID string) {
	if portal == nil || timestamp.IsZero() {
		return
	}
	meta, _ := portal.Metadata.(*PortalMetadata)
	if meta == nil {
		meta = &PortalMetadata{}
		portal.Metadata = meta
	}
	timestamp = timestamp.UTC()
	if shouldUpdateHistoryProgress(meta.LastSyncedAt, meta.LastMessageID, timestamp, messageID) {
		meta.LastSyncedAt = &timestamp
		if messageID != "" {
			meta.LastMessageID = messageID
		}
		if serverMessageID != "" {
			meta.LastServerMessageID = serverMessageID
		}
		if err := portal.Save(ctx); err != nil {
			nc.log.Err(err).Str("portal_id", string(portal.ID)).Msg("Failed to save portal history progress")
		}
	}
	loginMeta, _ := nc.login.Metadata.(*LoginMetadata)
	if loginMeta != nil && shouldUpdateHistoryProgress(loginMeta.LastSyncAt, "", timestamp, "") {
		loginMeta.LastSyncAt = &timestamp
		if err := nc.login.Save(ctx); err != nil {
			nc.log.Err(err).Msg("Failed to save login history progress")
		}
	}
}

func (nc *MyNetworkClient) historySyncStartForPortal(ctx context.Context, portalKey networkid.PortalKey) time.Time {
	if nc.bridge == nil {
		return nc.defaultAutomaticCatchupStart()
	}
	portal, err := nc.bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil {
		nc.log.Debug().Err(err).Str("portal_id", string(portalKey.ID)).Msg("Failed to load portal metadata for history sync")
	}
	if portal != nil {
		if meta, ok := portal.Metadata.(*PortalMetadata); ok && meta != nil && meta.LastSyncedAt != nil && !meta.LastSyncedAt.IsZero() {
			return meta.LastSyncedAt.UTC().Add(-historySyncOverlap)
		}
	}
	maxTS := nc.sessionEstablishedAt
	if maxTS.IsZero() {
		maxTS = time.Now().UTC()
	}
	msg, err := nc.bridge.DB.Message.GetLastNonFakePartAtOrBeforeTime(ctx, portalKey, maxTS)
	if err == nil && msg != nil && !msg.Timestamp.IsZero() {
		return msg.Timestamp.UTC().Add(-historySyncOverlap)
	}
	return nc.defaultAutomaticCatchupStart()
}

func (nc *MyNetworkClient) defaultAutomaticCatchupStart() time.Time {
	end := nc.sessionEstablishedAt
	if end.IsZero() {
		end = time.Now().UTC()
	}
	return end.Add(-automaticCatchupLookback)
}

func (nc *MyNetworkClient) historySyncStartForDMs() time.Time {
	loginMeta, _ := nc.login.Metadata.(*LoginMetadata)
	if loginMeta != nil && loginMeta.LastSyncAt != nil && !loginMeta.LastSyncAt.IsZero() {
		return loginMeta.LastSyncAt.UTC().Add(-historySyncOverlap)
	}
	return nc.defaultAutomaticCatchupStart()
}

func (nc *MyNetworkClient) triggerAutomaticDMCatchup() {
	go nc.runAutomaticDMCatchup(context.Background())
}

func (nc *MyNetworkClient) triggerAutomaticMUCCatchup() {
	go nc.runAutomaticMUCCatchup(context.Background())
}

func (nc *MyNetworkClient) runAutomaticDMCatchup(ctx context.Context) {
	if !nc.isXMPPConnected() || nc.bridge == nil || nc.login == nil {
		return
	}
	nc.syncMu.Lock()
	fullSyncRequested := nc.fullSyncRequested
	nc.syncMu.Unlock()
	if fullSyncRequested {
		return
	}
	nc.scheduleDMBackfill("", nc.historySyncStartForDMs())
}

func (nc *MyNetworkClient) runAutomaticMUCCatchup(ctx context.Context) {
	if !nc.isXMPPConnected() || nc.bridge == nil || nc.login == nil {
		return
	}
	portals, err := nc.bridge.DB.UserPortal.GetAllForLogin(ctx, nc.login.UserLogin)
	if err != nil {
		nc.log.Err(err).Msg("Failed to enumerate portals for automatic MUC catchup")
		return
	}
	for _, userPortal := range portals {
		if userPortal == nil || userPortal.Portal.ID == "" {
			continue
		}
		portal, err := nc.bridge.GetExistingPortalByKey(ctx, userPortal.Portal)
		if err != nil || portal == nil {
			continue
		}
		if !portalUsesGroupChat(portal) {
			continue
		}
		roomJID := UnescapeJID(string(userPortal.Portal.ID))
		if roomJID == "" {
			continue
		}
		if nc.mucRooms == nil {
			nc.mucRooms = map[string]bool{}
		}
		nc.mucRooms[roomJID] = true
		start := nc.historySyncStartForPortal(ctx, userPortal.Portal)
		nc.scheduleMUCBackfill(roomJID, start)
	}
}
