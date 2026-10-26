// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gosrc.io/xmpp/stanza"
)

// XEP-0402 write path. Every bookmark write is a read-modify-write: the
// server's current item is fetched first so that a name, password or nick
// written by another client survives, and the publish carries
// <publish-options> so the node stays private (other clients' room lists and
// room passwords must never become public). A publish that the server refuses
// in a way that means "this account cannot write private bookmarks" disables
// bookmark writes for the session.

const (
	// bookmarkWriteTimeout caps one fetch plus publish.
	bookmarkWriteTimeout = 20 * time.Second
	// bookmarkFormNS is XEP-0004 data forms.
	bookmarkFormNS = "jabber:x:data"
	// bookmarkPublishOptionsFormType is the FORM_TYPE of the publish-options form.
	bookmarkPublishOptionsFormType = "http://jabber.org/protocol/pubsub#publish-options"
	// bookmarkPublishOptionsFeature is the disco feature of servers that honour <publish-options>.
	bookmarkPublishOptionsFeature = bookmarkPublishOptionsFormType
	// bookmarkFetchMaxItems caps one bookmarks node fetch.
	bookmarkFetchMaxItems = 1000
	// bookmarkBackfillPace paces the publishes of one backfill run.
	bookmarkBackfillPace = 200 * time.Millisecond
	// bookmarkBackfillSettle is how long the bridge waits after connecting
	// before creating bookmarks for already-bridged rooms, so joins have
	// confirmed the occupant nicks first.
	bookmarkBackfillSettle = 5 * time.Second
)

// bookmarkRoomKey is the key every bookmark comparison uses: the lower-cased
// bare room JID.
func bookmarkRoomKey(jid string) string {
	jid = strings.TrimSpace(UnescapeJID(jid))
	if i := strings.IndexByte(jid, '/'); i >= 0 {
		jid = jid[:i]
	}
	return strings.ToLower(jid)
}

// bookmarkRoomLocalpart returns the localpart of a room JID (used as the
// fallback bookmark name).
func bookmarkRoomLocalpart(roomKey string) string {
	if i := strings.IndexByte(roomKey, '@'); i > 0 {
		return roomKey[:i]
	}
	return roomKey
}

// autojoinString renders an autojoin attribute value.
func autojoinString(autojoin bool) string {
	if autojoin {
		return "true"
	}
	return "false"
}

// mergeBookmark2 applies a bridge-side change onto the server's current item
// (nil when there is none) and reports whether anything changed (when not, the
// publish is skipped). Name, password and extensions of an existing item are
// never overwritten; an existing non-empty nick is never overwritten either.
// A missing name defaults to the room localpart, only when the item is created
// or the publish happens anyway.
func mergeBookmark2(existing *BookmarkConference, update BookmarkConference) (BookmarkConference, bool) {
	roomKey := bookmarkRoomKey(update.JID)
	if existing == nil {
		out := BookmarkConference{
			JID:      strings.TrimSpace(update.JID),
			Name:     strings.TrimSpace(update.Name),
			Autojoin: autojoinString(update.AutojoinEnabled()),
			Nick:     strings.TrimSpace(update.Nick),
			Password: strings.TrimSpace(update.Password),
		}
		if out.Name == "" {
			out.Name = bookmarkRoomLocalpart(roomKey)
		}
		return out, true
	}
	out := *existing
	changed := false
	if out.AutojoinEnabled() != update.AutojoinEnabled() {
		out.Autojoin = autojoinString(update.AutojoinEnabled())
		changed = true
	}
	if strings.TrimSpace(out.Nick) == "" && strings.TrimSpace(update.Nick) != "" {
		out.Nick = strings.TrimSpace(update.Nick)
		changed = true
	}
	if changed && strings.TrimSpace(out.Name) == "" {
		out.Name = bookmarkRoomLocalpart(roomKey)
	}
	return out, changed
}

// bookmarkPublishOptionsForm is the publish-options form published with every
// bookmark write: persistent, private, no last-item notifications.
func bookmarkPublishOptionsForm() *stanza.Form {
	return &stanza.Form{
		Type: "submit",
		Fields: []*stanza.Field{
			{Var: "FORM_TYPE", Type: "hidden", ValuesList: []string{bookmarkPublishOptionsFormType}},
			{Var: "pubsub#persist_items", ValuesList: []string{"true"}},
			{Var: "pubsub#max_items", ValuesList: []string{"max"}},
			{Var: "pubsub#send_last_published_item", ValuesList: []string{"never"}},
			{Var: "pubsub#access_model", ValuesList: []string{"whitelist"}},
		},
	}
}

// bookmarkPublishError wraps an IQ error answer of a bookmark publish so the
// classification below can inspect it.
type bookmarkPublishError struct {
	reason string
	code   int
	text   string
}

func (e *bookmarkPublishError) Error() string {
	msg := e.reason
	if msg == "" {
		msg = e.text
	}
	if msg == "" && e.code != 0 {
		msg = fmt.Sprintf("IQ error %d", e.code)
	}
	if msg == "" {
		msg = "unknown IQ error"
	}
	return "bookmark publish failed: " + msg
}

// bookmarkErrorDisablesWrites classifies an IQ error (condition reason and/or
// numeric code): an error that means "this server or node will not take
// private writes" (precondition not met, unsupported publish-options,
// forbidden) disables writes for the session. Network errors and timeouts do
// not.
func bookmarkErrorDisablesWrites(reason string, code int) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "conflict", "feature-not-implemented", "bad-request", "forbidden", "not-allowed":
		return true
	}
	switch code {
	case 400, 403, 405, 409, 501:
		return true
	}
	return false
}

// bookmarkIQErrorDisablesWrites classifies an IQ error answer.
func bookmarkIQErrorDisablesWrites(resp *stanza.IQ) bool {
	if resp == nil || resp.Error == nil {
		return false
	}
	return bookmarkErrorDisablesWrites(resp.Error.Reason, resp.Error.Code)
}

// bookmarkPublishDisablesWrites reports whether a publish error should stop
// bookmark writes for this session.
func bookmarkPublishDisablesWrites(err error) bool {
	var pubErr *bookmarkPublishError
	return errors.As(err, &pubErr) && bookmarkErrorDisablesWrites(pubErr.reason, pubErr.code)
}

// bookmarkWritesDisabledNow reports whether a previous publish showed that this
// account cannot write private bookmarks.
func (nc *MyNetworkClient) bookmarkWritesDisabledNow() bool {
	nc.bookmarkStateMu.Lock()
	defer nc.bookmarkStateMu.Unlock()
	return nc.bookmarkWritesOff
}

// disableBookmarkWrites flips the session flag and reports whether this call
// was the first to do so.
func (nc *MyNetworkClient) disableBookmarkWrites() bool {
	nc.bookmarkStateMu.Lock()
	defer nc.bookmarkStateMu.Unlock()
	first := !nc.bookmarkWritesOff
	nc.bookmarkWritesOff = true
	return first
}

// bookmarkGate checks once per session whether the account (its PEP service)
// advertises bookmark reads and publish-options writes. A failed check is not
// cached: it fails open, because every publish carries publish-options anyway,
// so a server that refuses private writes rejects the publish itself.
func (nc *MyNetworkClient) bookmarkGate(ctx context.Context) (read, write bool) {
	if nc == nil || nc.bridge == nil || nc.xmppClient == nil {
		return true, true
	}
	nc.bookmarkStateMu.Lock()
	if nc.bookmarkGateKnown {
		r, w := nc.bookmarkGateRead, nc.bookmarkGateWrite
		nc.bookmarkStateMu.Unlock()
		return r, w
	}
	nc.bookmarkStateMu.Unlock()

	own := nc.ownBareJID()
	if own == "" {
		return true, true
	}
	info, err := nc.requestDiscoInfo(ctx, own)
	if err != nil || info == nil {
		nc.log.Debug().Err(err).Msg("Bookmarks: capability check failed; will retry")
		return true, true
	}
	readOK, writeOK := false, false
	for _, feature := range info.Features {
		v := strings.ToLower(strings.TrimSpace(feature.Var))
		if strings.HasPrefix(v, NSBookmarks2) {
			readOK = true
		}
		if v == bookmarkPublishOptionsFeature {
			writeOK = true
		}
	}
	writeOK = writeOK && readOK

	nc.bookmarkStateMu.Lock()
	nc.bookmarkGateKnown, nc.bookmarkGateRead, nc.bookmarkGateWrite = true, readOK, writeOK
	nc.bookmarkStateMu.Unlock()
	if !readOK {
		nc.log.Info().Msg("Bookmarks: the XMPP account does not advertise XEP-0402 support")
	}
	return readOK, writeOK
}

// bookmarkCacheStore replaces the session cache with a fresh fetch.
func (nc *MyNetworkClient) bookmarkCacheStore(items map[string]BookmarkConference) {
	nc.bookmarkStateMu.Lock()
	defer nc.bookmarkStateMu.Unlock()
	nc.bookmarkCache = items
	nc.bookmarkCacheValid = true
}

// parseBookmark2Items decodes the items of a bookmarks node fetch.
func parseBookmark2Items(list []stanza.Item) map[string]BookmarkConference {
	items := make(map[string]BookmarkConference, len(list))
	for _, item := range list {
		if item.Any == nil {
			continue
		}
		if item.Any.XMLName.Space != NSBookmarks2 || item.Any.XMLName.Local != "conference" {
			continue
		}
		data, err := xml.Marshal(item.Any)
		if err != nil {
			continue
		}
		var conf BookmarkConference2
		if err := xml.Unmarshal(data, &conf); err != nil {
			continue
		}
		jid := strings.TrimSpace(conf.JID)
		if jid == "" {
			jid = strings.TrimSpace(item.Id)
		}
		key := bookmarkRoomKey(jid)
		if key == "" {
			continue
		}
		items[key] = BookmarkConference{
			JID:      jid,
			Name:     conf.Name,
			Autojoin: conf.Autojoin,
			Nick:     conf.Nick,
			Password: conf.Password,
		}
	}
	return items
}

// fetchBookmark2Items fetches the whole XEP-0402 node, keyed by lower-cased
// bare room JID.
func (nc *MyNetworkClient) fetchBookmark2Items(ctx context.Context) (map[string]BookmarkConference, error) {
	own := nc.ownBareJID()
	if own == "" {
		return nil, fmt.Errorf("own bare JID missing")
	}
	if nc.xmppClient == nil {
		return nil, fmt.Errorf("not connected to XMPP")
	}
	iq, err := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		To:   own,
		Id:   fmt.Sprintf("bookmarks2_fetch_%d", time.Now().UnixNano()),
	})
	if err != nil {
		return nil, err
	}
	iq.Payload = &stanza.PubSubGeneric{Items: &stanza.Items{Node: NSBookmarks2, MaxItems: bookmarkFetchMaxItems}}
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp.Type == stanza.IQTypeError {
			return nil, fmt.Errorf("bookmarks fetch failed: %s", bookmarkIQErrorText(resp))
		}
		payload, ok := resp.Payload.(*stanza.PubSubGeneric)
		if !ok || payload.Items == nil {
			return map[string]BookmarkConference{}, nil
		}
		return parseBookmark2Items(payload.Items.List), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// bookmarkIQErrorText renders an IQ error answer for logs and errors.
func bookmarkIQErrorText(resp stanza.IQ) string {
	if resp.Error == nil {
		return "unknown error"
	}
	parts := make([]string, 0, 3)
	if resp.Error.Reason != "" {
		parts = append(parts, resp.Error.Reason)
	}
	if resp.Error.Code != 0 {
		parts = append(parts, fmt.Sprintf("%d", resp.Error.Code))
	}
	if resp.Error.Text != "" {
		parts = append(parts, resp.Error.Text)
	}
	if len(parts) == 0 {
		return "unknown error"
	}
	return strings.Join(parts, " ")
}

// publishBookmarkIQ publishes one bookmark item with publish-options.
func (nc *MyNetworkClient) publishBookmarkIQ(ctx context.Context, conf BookmarkConference) error {
	service := nc.ownBareJID()
	if service == "" {
		return fmt.Errorf("own bare JID missing")
	}
	jid := strings.TrimSpace(conf.JID)
	if jid == "" {
		return fmt.Errorf("bookmark JID missing")
	}
	if nc.xmppClient == nil {
		return fmt.Errorf("not connected to XMPP")
	}
	attrs := []xml.Attr{{Name: xml.Name{Local: "jid"}, Value: jid}}
	if conf.Name != "" {
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "name"}, Value: conf.Name})
	}
	if conf.Autojoin != "" {
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "autojoin"}, Value: conf.Autojoin})
	}
	if conf.Nick != "" {
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "nick"}, Value: conf.Nick})
	}
	if conf.Password != "" {
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "password"}, Value: conf.Password})
	}
	iq, err := stanza.NewPublishItemOptsRq(service, NSBookmarks2, []stanza.Item{{
		Id: jid,
		Any: &stanza.Node{
			XMLName: xml.Name{Space: NSBookmarks2, Local: "conference"},
			Attrs:   attrs,
		},
	}}, &stanza.PublishOptions{Form: bookmarkPublishOptionsForm()})
	if err != nil {
		return err
	}
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Type == stanza.IQTypeError {
			e := resp.Error
			pubErr := &bookmarkPublishError{}
			if e != nil {
				pubErr.reason = strings.ToLower(strings.TrimSpace(e.Reason))
				pubErr.code = e.Code
				pubErr.text = e.Text
			}
			return pubErr
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// publishBookmark2Merged performs the read-modify-write publish of one
// bookmark: fetch the server's current item, merge the requested change (so
// other clients' name/password/nick survive), publish with publish-options.
// create=false leaves a missing bookmark alone (used when leaving a room).
// A skipped write (gate or session-disabled) is not an error.
func (nc *MyNetworkClient) publishBookmark2Merged(ctx context.Context, conf BookmarkConference, create bool) error {
	key := bookmarkRoomKey(conf.JID)
	if key == "" {
		return fmt.Errorf("bookmark JID missing")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !nc.bookmarkWritesDisabledNow() {
		if _, write := nc.bookmarkGate(ctx); !write {
			return nil
		}
	} else {
		return nil
	}

	nc.bookmarkWriteMu.Lock()
	defer nc.bookmarkWriteMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, bookmarkWriteTimeout)
	defer cancel()

	items, err := nc.fetchBookmark2Items(ctx)
	if err != nil {
		return fmt.Errorf("bookmark fetch before write failed: %w", err)
	}
	nc.bookmarkCacheStore(items)

	var existing *BookmarkConference
	if current, ok := items[key]; ok {
		existing = &current
	} else if !create {
		return nil
	}
	merged, changed := mergeBookmark2(existing, conf)
	if !changed {
		return nil
	}
	if err := nc.publishBookmarkIQ(ctx, merged); err != nil {
		if bookmarkPublishDisablesWrites(err) && nc.disableBookmarkWrites() {
			nc.log.Warn().Err(err).Msg("Bookmarks: the server refused a private bookmark write (non-private node configuration or no publish-options); bookmark writes are disabled for this session")
		}
		return err
	}
	nc.bookmarkStateMu.Lock()
	if nc.bookmarkCache == nil {
		nc.bookmarkCache = map[string]BookmarkConference{}
	}
	nc.bookmarkCache[key] = merged
	nc.bookmarkCacheValid = true
	nc.bookmarkStateMu.Unlock()
	return nil
}

// bridgedMUCBookmarkCandidates lists the MUC rooms this login is bridged into
// (portal with a Matrix room, not left, not invite-pending), sorted.
func (nc *MyNetworkClient) bridgedMUCBookmarkCandidates(ctx context.Context) []string {
	if nc == nil || nc.bridge == nil || nc.bridge.DB == nil || nc.login == nil {
		return nil
	}
	rows, err := nc.bridge.DB.UserPortal.GetAllForLogin(ctx, nc.login.UserLogin)
	if err != nil {
		nc.log.Warn().Err(err).Msg("Bookmarks: failed to list portals for the backfill")
		return nil
	}
	seen := map[string]bool{}
	rooms := make([]string, 0, len(rows))
	for _, row := range rows {
		key := row.Portal
		if key.Receiver != "" && key.Receiver != nc.login.ID {
			continue
		}
		portal, err := nc.bridge.GetExistingPortalByKey(ctx, key)
		if err != nil || portal == nil || portal.MXID == "" {
			continue
		}
		if meta, ok := portal.Metadata.(*PortalMetadata); ok && meta != nil && (meta.MUCLeft || meta.MUCInvitePending) {
			continue
		}
		jid := strings.TrimSpace(UnescapeJID(string(portal.ID)))
		if jid == "" || isCheogramContactJID(jid) {
			continue
		}
		roomKey := bookmarkRoomKey(jid)
		if roomKey == "" || seen[roomKey] {
			continue
		}
		seen[roomKey] = true
		rooms = append(rooms, roomKey)
	}
	sort.Strings(rooms)
	return rooms
}

// runBookmarkBackfill creates autojoin=true bookmarks for the rooms this
// bridge is already in and that have none, so the user's other clients see
// them too. It runs once per login (persisted marker) unless force is set,
// only when the account supports private writes, and never touches an
// existing bookmark.
func (nc *MyNetworkClient) runBookmarkBackfill(ctx context.Context, force bool) (created int) {
	if nc == nil || nc.bridge == nil || nc.login == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	if meta != nil && meta.BookmarksBackfilled && !force {
		return 0
	}
	if !nc.isXMPPConnected() {
		return 0
	}
	if !nc.bookmarkWritesDisabledNow() {
		if _, write := nc.bookmarkGate(ctx); !write {
			return 0
		}
	} else {
		return 0
	}

	nc.bookmarkWriteMu.Lock()
	defer nc.bookmarkWriteMu.Unlock()

	fctx, cancel := context.WithTimeout(ctx, bookmarkWriteTimeout)
	items, err := nc.fetchBookmark2Items(fctx)
	cancel()
	if err != nil {
		nc.log.Warn().Err(err).Msg("Bookmarks: fetch before backfill failed; backfill postponed")
		return 0
	}
	nc.bookmarkCacheStore(items)

	rooms := nc.bridgedMUCBookmarkCandidates(ctx)
	failed := 0
	for i, room := range rooms {
		if _, ok := items[room]; ok {
			continue
		}
		if i > 0 && !sleepCtx(ctx, bookmarkBackfillPace) {
			break
		}
		merged, _ := mergeBookmark2(nil, BookmarkConference{
			JID:      room,
			Autojoin: "true",
			Nick:     nc.mucNickOr(room),
		})
		pctx, pcancel := context.WithTimeout(ctx, bookmarkWriteTimeout)
		err := nc.publishBookmarkIQ(pctx, merged)
		pcancel()
		if err != nil {
			failed++
			if bookmarkPublishDisablesWrites(err) {
				if nc.disableBookmarkWrites() {
					nc.log.Warn().Err(err).Msg("Bookmarks: the server refused a private bookmark write; bookmark writes are disabled for this session")
				}
				break
			}
			nc.log.Warn().Err(err).Str("room_jid", room).Msg("Bookmarks: backfill publish failed")
			continue
		}
		created++
	}
	nc.log.Info().Int("rooms", len(rooms)).Int("created", created).Int("failed", failed).Bool("forced", force).Msg("Bookmarks: backfill finished")

	if failed == 0 && ctx.Err() == nil {
		if meta == nil {
			meta = &LoginMetadata{}
		}
		meta.BookmarksBackfilled = true
		nc.login.Metadata = meta
		if err := nc.login.Save(ctx); err != nil {
			nc.log.Warn().Err(err).Msg("Bookmarks: failed to persist the backfill marker; the backfill will run again next session")
		}
		if created > 0 && !force {
			nc.notifyManagement(fmt.Sprintf("Created bookmarks (auto-join on) for %d room(s) this bridge is already in, so your other clients see them too. Existing bookmarks were not changed.", created))
		}
	}
	return created
}

// scheduleBookmarkBackfill runs the one-time backfill shortly after a
// connection is ready (so joins have confirmed occupant nicks), once per login.
func (nc *MyNetworkClient) scheduleBookmarkBackfill() {
	if nc == nil || nc.login == nil {
		return
	}
	if meta, ok := nc.login.Metadata.(*LoginMetadata); ok && meta != nil && meta.BookmarksBackfilled {
		return
	}
	if !sleepCtx(context.Background(), bookmarkBackfillSettle) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	nc.runBookmarkBackfill(ctx, false)
}

// sleepCtx sleeps for d unless the context ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if ctx == nil {
		time.Sleep(d)
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
