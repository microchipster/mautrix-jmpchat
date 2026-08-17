package connector

import (
	"encoding/xml"
	"fmt"
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

func (nc *MyNetworkClient) publishBookmark2(conf BookmarkConference) error {
	service := nc.ownBareJID()
	if service == "" {
		return fmt.Errorf("own bare JID missing")
	}
	conf.JID = strings.TrimSpace(conf.JID)
	if conf.JID == "" {
		return fmt.Errorf("bookmark JID missing")
	}
	attrs := []xml.Attr{{Name: xml.Name{Local: "jid"}, Value: conf.JID}}
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
	iq, err := stanza.NewPublishItemRq(service, NSBookmarks2, conf.JID, stanza.Item{
		Any: &stanza.Node{
			XMLName: xml.Name{Space: NSBookmarks2, Local: "conference"},
			Attrs:   attrs,
		},
	})
	if err != nil {
		return err
	}
	return nc.sendXMPP(iq)
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
