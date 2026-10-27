package connector

import (
	"context"
	"encoding/xml"
	"sort"
	"strings"

	"gosrc.io/xmpp/stanza"
)

const (
	NSBookmarks2          = "urn:xmpp:bookmarks:1"
	NSBookmarksConversion = "urn:xmpp:bookmarks-conversion:0"
)

// BookmarkStorage implements XEP-0048 (Bookmarks).
// It is used to discover group chats on login.
type BookmarkStorage struct {
	XMLName     xml.Name             `xml:"storage:bookmarks storage"`
	Conferences []BookmarkConference `xml:"conference"`
}

// PrivateBookmarkQuery wraps BookmarkStorage for XEP-0048 private XML storage.
type PrivateBookmarkQuery struct {
	XMLName xml.Name         `xml:"jabber:iq:private query"`
	Storage *BookmarkStorage `xml:"storage:bookmarks storage"`
}

func (b *PrivateBookmarkQuery) Namespace() string {
	return b.XMLName.Space
}

func (b *PrivateBookmarkQuery) GetSet() *stanza.ResultSet {
	return nil
}

func (b *BookmarkStorage) Namespace() string {
	return b.XMLName.Space
}

func (b *BookmarkStorage) GetSet() *stanza.ResultSet {
	return nil
}

type BookmarkConference struct {
	JID      string `xml:"jid,attr"`
	Name     string `xml:"name,attr,omitempty"`
	Autojoin string `xml:"autojoin,attr,omitempty"`
	Nick     string `xml:"nick,omitempty"`
	Password string `xml:"password,omitempty"`
	// ItemID is the pubsub item ID a XEP-0402 bookmark was fetched under.
	// Items should use the room JID, but servers may use another ID; keeping
	// it lets a retraction target the item that actually exists. Never
	// marshalled.
	ItemID string `xml:"-"`
}

// BookmarkConference2 implements XEP-0402 (Bookmarks 2).
type BookmarkConference2 struct {
	XMLName  xml.Name `xml:"urn:xmpp:bookmarks:1 conference"`
	JID      string   `xml:"jid,attr"`
	Name     string   `xml:"name,attr,omitempty"`
	Autojoin string   `xml:"autojoin,attr,omitempty"`
	Nick     string   `xml:"nick,omitempty"`
	Password string   `xml:"password,omitempty"`
}

// bookmarkSnapshot returns the sorted list of room JIDs that are currently
// treated as bookmarked for this connection.
func (nc *MyNetworkClient) bookmarkSnapshot() []string {
	if nc.bookmarkedRooms == nil {
		return nil
	}
	rooms := make([]string, 0, len(nc.bookmarkedRooms))
	for jid := range nc.bookmarkedRooms {
		if jid = strings.TrimSpace(jid); jid != "" {
			rooms = append(rooms, jid)
		}
	}
	sort.Strings(rooms)
	return rooms
}

// pushBookmarks republishes every known bookmark as a XEP-0402 entry,
// keeping the remembered name and nickname. Each publish is a read-modify-write
// merge, so other clients' name/password/nick survive. Returns how many
// publishes succeeded.
func (nc *MyNetworkClient) pushBookmarks(ctx context.Context) int {
	pushed := 0
	for _, jid := range nc.bookmarkSnapshot() {
		name := ""
		if nc.bookmarkedRoomNames != nil {
			name = nc.bookmarkedRoomNames[jid]
		}
		if err := nc.publishBookmark2Merged(ctx, BookmarkConference{
			JID:      jid,
			Name:     name,
			Autojoin: "true",
			Nick:     nc.mucNickOr(jid),
		}, true); err != nil {
			nc.log.Warn().Err(err).Str("room_jid", jid).Msg("Failed to push bookmark")
			continue
		}
		pushed++
	}
	return pushed
}

func (bc BookmarkConference) AutojoinEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(bc.Autojoin)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func init() {
	stanza.TypeRegistry.MapExtension(
		stanza.PKTIQ,
		xml.Name{Space: "storage:bookmarks", Local: "storage"},
		BookmarkStorage{},
	)
	stanza.TypeRegistry.MapExtension(
		stanza.PKTIQ,
		xml.Name{Space: "jabber:iq:private", Local: "query"},
		PrivateBookmarkQuery{},
	)
}
