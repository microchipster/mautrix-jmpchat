package connector

import (
	"encoding/xml"
	"strings"

	"gosrc.io/xmpp/stanza"
)

// BookmarkStorage implements XEP-0048 (Bookmarks).
// It is used to discover group chats on login.
type BookmarkStorage struct {
	XMLName     xml.Name             `xml:"storage:bookmarks storage"`
	Conferences []BookmarkConference `xml:"conference"`
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
}
