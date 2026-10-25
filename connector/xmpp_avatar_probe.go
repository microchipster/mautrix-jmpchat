// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2/commands"
)

const (
	// NSAvatarMetadata is the XEP-0084 metadata node.
	NSAvatarMetadata = "urn:xmpp:avatar:metadata"
	// NSAvatarData is the XEP-0084 data node.
	NSAvatarData = "urn:xmpp:avatar:data"

	avatarProbeTimeout = 15 * time.Second
)

type avatarMetadata struct {
	XMLName xml.Name     `xml:"urn:xmpp:avatar:metadata metadata"`
	Info    []avatarInfo `xml:"info"`
}

type avatarInfo struct {
	ID     string `xml:"id,attr,omitempty"`
	Type   string `xml:"type,attr,omitempty"`
	Bytes  int    `xml:"bytes,attr,omitempty"`
	Height int    `xml:"height,attr,omitempty"`
	Width  int    `xml:"width,attr,omitempty"`
}

// parseAvatarMetadata decodes an XEP-0084 metadata element.
func parseAvatarMetadata(raw []byte) (avatarMetadata, bool) {
	var meta avatarMetadata
	if err := xml.Unmarshal(raw, &meta); err != nil {
		return avatarMetadata{}, false
	}
	if len(meta.Info) == 0 {
		return meta, false
	}
	return meta, true
}

// describeAvatarMetadata renders the first info element for probe output.
func describeAvatarMetadata(meta avatarMetadata) string {
	if len(meta.Info) == 0 {
		return ""
	}
	info := meta.Info[0]
	parts := make([]string, 0, 4)
	if info.ID != "" {
		parts = append(parts, "id="+info.ID)
	}
	if info.Bytes > 0 {
		parts = append(parts, fmt.Sprintf("%d bytes", info.Bytes))
	}
	if info.Width > 0 || info.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dx%d", info.Width, info.Height))
	}
	if info.Type != "" {
		parts = append(parts, info.Type)
	}
	if len(parts) == 0 {
		return "metadata present"
	}
	return strings.Join(parts, ", ")
}

// probeVCardTemp fetches the vcard-temp photo and reports size, mime and timing.
func (nc *MyNetworkClient) probeVCardTemp(ctx context.Context, jid string) string {
	start := time.Now()
	data, mime, err := nc.fetchVCardPhoto(ctx, jid)
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		return fmt.Sprintf("vcard-temp: error after %s: %v", elapsed, err)
	}
	if len(data) == 0 {
		return fmt.Sprintf("vcard-temp: no photo (%s)", elapsed)
	}
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return fmt.Sprintf("vcard-temp: ok (%d bytes, %s) in %s", len(data), mime, elapsed)
}

// probeXEP0084Metadata queries the XEP-0084 avatar metadata node and reports the first info.
func (nc *MyNetworkClient) probeXEP0084Metadata(ctx context.Context, jid string) string {
	start := time.Now()
	iq, err := stanza.NewIQ(stanza.Attrs{Type: stanza.IQTypeGet, To: jid, Id: fmt.Sprintf("avatar_meta_%d", time.Now().UnixNano())})
	if err != nil {
		return fmt.Sprintf("XEP-0084 metadata: error building request: %v", err)
	}
	iq.Payload = &stanza.PubSubGeneric{Items: &stanza.Items{Node: NSAvatarMetadata}}
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return fmt.Sprintf("XEP-0084 metadata: error sending request: %v", err)
	}
	select {
	case resp := <-ch:
		elapsed := time.Since(start).Round(time.Millisecond)
		if resp.Type != stanza.IQTypeResult {
			return fmt.Sprintf("XEP-0084 metadata: %s response in %s", resp.Type, elapsed)
		}
		items, ok := resp.Payload.(*stanza.PubSubGeneric)
		if !ok || items.Items == nil || len(items.Items.List) == 0 {
			return fmt.Sprintf("XEP-0084 metadata: no metadata (%s)", elapsed)
		}
		for _, item := range items.Items.List {
			if item.Any == nil {
				continue
			}
			raw, err := xml.Marshal(item.Any)
			if err != nil {
				continue
			}
			if meta, ok := parseAvatarMetadata(raw); ok {
				return fmt.Sprintf("XEP-0084 metadata: ok (%s) in %s", describeAvatarMetadata(meta), elapsed)
			}
		}
		return fmt.Sprintf("XEP-0084 metadata: no metadata (%s)", elapsed)
	case <-ctx.Done():
		return fmt.Sprintf("XEP-0084 metadata: timed out after %s", time.Since(start).Round(time.Millisecond))
	}
}

// probeDiscoInfo queries disco#info and reports identities/features and avatar support.
func (nc *MyNetworkClient) probeDiscoInfo(ctx context.Context, jid string) string {
	start := time.Now()
	iq, err := stanza.NewIQ(stanza.Attrs{Type: stanza.IQTypeGet, To: jid, Id: fmt.Sprintf("avatar_disco_%d", time.Now().UnixNano())})
	if err != nil {
		return fmt.Sprintf("disco#info: error building request: %v", err)
	}
	iq.Payload = &stanza.DiscoInfo{}
	ch, err := nc.xmppClient.SendIQ(ctx, iq)
	if err != nil {
		return fmt.Sprintf("disco#info: error sending request: %v", err)
	}
	select {
	case resp := <-ch:
		elapsed := time.Since(start).Round(time.Millisecond)
		if resp.Type != stanza.IQTypeResult {
			return fmt.Sprintf("disco#info: %s response in %s", resp.Type, elapsed)
		}
		info, ok := resp.Payload.(*stanza.DiscoInfo)
		if !ok {
			return fmt.Sprintf("disco#info: unexpected payload in %s", elapsed)
		}
		avatarFeatures := make([]string, 0, 2)
		for _, feature := range info.Features {
			if feature.Var == NSAvatarMetadata || feature.Var == NSAvatarData || feature.Var == NSAvatarMetadata+"+notify" {
				avatarFeatures = append(avatarFeatures, feature.Var)
			}
		}
		extra := ""
		if len(avatarFeatures) > 0 {
			extra = ", avatar: " + strings.Join(avatarFeatures, " ")
		}
		return fmt.Sprintf("disco#info: ok (%d identities, %d features%s) in %s", len(info.Identity), len(info.Features), extra, elapsed)
	case <-ctx.Done():
		return fmt.Sprintf("disco#info: timed out after %s", time.Since(start).Round(time.Millisecond))
	}
}

var CommandAvatarProbe = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		if len(ce.Args) == 0 {
			ce.Reply("Usage: avatar-probe <phone or JID>")
			return
		}
		jid := normalizeJID(ce.Args[0])
		if jid == "" {
			ce.Reply("Invalid JID")
			return
		}
		login := ce.User.GetDefaultLogin()
		if login == nil {
			ce.Reply("No login found")
			return
		}
		client, ok := login.Client.(*MyNetworkClient)
		if !ok || client == nil {
			ce.Reply("Network client not available")
			return
		}
		if !client.isXMPPConnected() {
			ce.Reply("Not connected to XMPP")
			return
		}
		ctx, cancel := context.WithTimeout(ce.Ctx, 3*avatarProbeTimeout)
		defer cancel()
		lines := []string{
			fmt.Sprintf("Avatar probe for %s:", jid),
			client.probeVCardTemp(ctx, jid),
			client.probeXEP0084Metadata(ctx, jid),
			client.probeDiscoInfo(ctx, jid),
		}
		ce.Reply(strings.Join(lines, "\n"))
	},
	Name: "avatar-probe",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Probe avatar sources (vcard-temp, XEP-0084, disco#info) for a contact",
	},
	RequiresLogin: true,
}
