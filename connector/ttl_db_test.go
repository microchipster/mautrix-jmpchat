// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

const ttlTestBridgeID = networkid.BridgeID("jmpchat")

// ttlTestDB is a real SQLite bridge database with the bridgev2 schema, used to
// exercise the raw SQL behind the history TTL cleanup.
type ttlTestDB struct {
	db     *database.Database
	bridge *bridgev2.Bridge
}

func newTTLTestDB(t *testing.T) *ttlTestDB {
	t.Helper()
	raw, err := dbutil.NewWithDialect("file:"+filepath.Join(t.TempDir(), "bridge.db"), "sqlite3")
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Errorf("close sqlite database: %v", err)
		}
	})
	connector := NewMyConnector(zerolog.Nop())
	db := database.New(ttlTestBridgeID, connector.GetDBMetaTypes(), raw)
	if err := raw.Upgrade(context.Background()); err != nil {
		t.Fatalf("upgrade bridge schema: %v", err)
	}
	return &ttlTestDB{
		db:     db,
		bridge: &bridgev2.Bridge{ID: ttlTestBridgeID, DB: db, Log: zerolog.Nop()},
	}
}

func (b *ttlTestDB) seedGhost(t *testing.T, ghostID string) {
	t.Helper()
	if err := b.db.Ghost.Insert(context.Background(), &database.Ghost{
		ID:       networkid.UserID(ghostID),
		Metadata: &GhostMetadata{},
	}); err != nil {
		t.Fatalf("insert ghost %q: %v", ghostID, err)
	}
}

func (b *ttlTestDB) seedPortal(t *testing.T, portalID networkid.PortalID, roomID id.RoomID, otherUser networkid.UserID, meta *PortalMetadata) *database.Portal {
	t.Helper()
	portal := &database.Portal{
		PortalKey:   networkid.PortalKey{ID: portalID},
		MXID:        roomID,
		RoomType:    database.RoomTypeDM,
		OtherUserID: otherUser,
		Metadata:    meta,
	}
	if err := b.db.Portal.Insert(context.Background(), portal); err != nil {
		t.Fatalf("insert portal %q: %v", portalID, err)
	}
	return portal
}

func (b *ttlTestDB) seedMessage(t *testing.T, portalID networkid.PortalID, messageID, partID string, sender networkid.UserID, ts time.Time, mxid id.EventID, meta *MessageMetadata) int64 {
	t.Helper()
	if mxid == "" {
		mxid = id.EventID("$" + strings.ReplaceAll(messageID, ":", "-") + "-part-" + partID)
	}
	msg := &database.Message{
		ID:        networkid.MessageID(messageID),
		PartID:    networkid.PartID(partID),
		MXID:      mxid,
		Room:      networkid.PortalKey{ID: portalID},
		SenderID:  sender,
		Timestamp: ts,
		Metadata:  meta,
	}
	if err := b.db.Message.Insert(context.Background(), msg); err != nil {
		t.Fatalf("insert message %q: %v", messageID, err)
	}
	return msg.RowID
}

func TestCollectTTLCandidatesAgainstDatabase(t *testing.T) {
	b := newTTLTestDB(t)
	now := time.Now().UTC()
	incoming := MakeUserID("+15551112222")
	b.seedGhost(t, string(incoming))
	dbPortal := b.seedPortal(t, "thread-scope-1-1", "!room1:example.com", incoming, &PortalMetadata{})
	portal := &bridgev2.Portal{Portal: dbPortal}
	contact := ttlContactForPortal(portal)
	if contact != "+15551112222@cheogram.com" {
		t.Fatalf("contact = %q, want +15551112222@cheogram.com", contact)
	}

	b.seedMessage(t, "thread-scope-1-1", "sms:1:300", "0", incoming, now.Add(-40*24*time.Hour), "", &MessageMetadata{Body: "old text"})
	b.seedMessage(t, "thread-scope-1-1", "mms:1:301", "0", incoming, now.Add(-40*24*time.Hour), "", &MessageMetadata{Body: "old media", HasAttachment: true})
	b.seedMessage(t, "thread-scope-1-1", "sms:1:302", "0", incoming, now.Add(-1*time.Hour), "", &MessageMetadata{Body: "Your verification code is 123456"})

	cfg := DefaultHistoryTTLConfig()
	cfg.Enabled = true
	cfg.Global = TTLDuration(30 * 24 * time.Hour)
	cfg.Patterns = []string{"otp"}
	cfg.DeleteMedia = false
	cfg.ScanLimit = 100
	ttl, hasTTL := resolveTTLFor(ttlPolicy{Global: 30 * 24 * time.Hour}, portal.MXID, contact)
	connector := &MyConnector{bridge: b.bridge, log: zerolog.Nop(), config: &SimpleConfig{HistoryTTL: cfg}}

	candidates, err := connector.collectTTLCandidates(context.Background(), portal, cfg, contact, ttl, hasTTL, true, now)
	if err != nil {
		t.Fatalf("collectTTLCandidates error: %v", err)
	}
	// Old media is excluded by the no-media SQL filter, the recent OTP message
	// comes from the pattern scan window.
	if len(candidates) != 2 {
		t.Fatalf("candidates = %+v, want old text + recent OTP", candidates)
	}
	policy := ttlPolicy{
		Global:      30 * 24 * time.Hour,
		DeleteMedia: false,
		Patterns:    mustCompileTTLPatterns(t, []string{"otp"}, nil),
	}
	decisions := planTTLRedactions(policy, candidates, now)
	reasons := make(map[int64]string, len(decisions))
	for _, decision := range decisions {
		reasons[decision.Candidate.RowID] = decision.Reason
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions = %+v, want otp + ttl", decisions)
	}
	for rowID, reason := range reasons {
		if reason != "ttl" && reason != "pattern:otp" {
			t.Errorf("row %d reason = %q", rowID, reason)
		}
	}

	// Deleting a redacted message removes its bridge row. The bridge bot is
	// nil here, so no Matrix redaction is attempted.
	var ttlDecisionRow ttlDecision
	for _, decision := range decisions {
		if decision.Reason == "ttl" {
			ttlDecisionRow = decision
		}
	}
	if ttlDecisionRow.Candidate.RowID == 0 {
		t.Fatal("no ttl decision found")
	}
	if err := connector.redactTTLMessage(context.Background(), portal, ttlDecisionRow); err != nil {
		t.Fatalf("redactTTLMessage error: %v", err)
	}
	if msg, err := b.db.Message.GetByRowID(context.Background(), ttlDecisionRow.Candidate.RowID); err != nil || msg != nil {
		t.Errorf("deleted row still present: %v, %v", msg, err)
	}

	// Fake event IDs only exist in the bridge database; the row is still removed.
	fakeRowID := b.seedMessage(t, "thread-scope-1-1", "sms:1:303", "0", incoming, now.Add(-40*24*time.Hour), id.EventID(database.FakeMXIDPrefix+"placeholder"), &MessageMetadata{Body: "fake row"})
	if err := connector.redactTTLMessage(context.Background(), portal, ttlDecision{Candidate: ttlCandidate{RowID: fakeRowID, MXID: id.EventID(database.FakeMXIDPrefix + "placeholder")}, Reason: "ttl"}); err != nil {
		t.Fatalf("redactTTLMessage (fake MXID) error: %v", err)
	}
	if msg, err := b.db.Message.GetByRowID(context.Background(), fakeRowID); err != nil || msg != nil {
		t.Errorf("fake row still present: %v, %v", msg, err)
	}

	// With delete_media enabled the old MMS is also a candidate.
	cfg.DeleteMedia = true
	candidates, err = connector.collectTTLCandidates(context.Background(), portal, cfg, contact, ttl, hasTTL, true, now)
	if err != nil {
		t.Fatalf("collectTTLCandidates (with media) error: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates with media = %+v, want old media + recent OTP (old text was deleted)", candidates)
	}

	// scan_limit bounds how many rows one pass inspects. The oldest row was
	// deleted above and old media is filtered out, so the only candidate left
	// is the newest pattern-window row.
	cfg.DeleteMedia = false
	cfg.ScanLimit = 1
	candidates, err = connector.collectTTLCandidates(context.Background(), portal, cfg, contact, ttl, hasTTL, true, now)
	if err != nil {
		t.Fatalf("collectTTLCandidates (limited) error: %v", err)
	}
	if len(candidates) != 1 || !strings.Contains(candidates[0].Body, "verification code") {
		t.Fatalf("limited candidates = %+v, want only the newest pattern-window row", candidates)
	}
}
