package connector

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/bridgev2"
)

const NSHTTPUpload = "urn:xmpp:http:upload:0"

type HTTPUploadRequest struct {
	XMLName     xml.Name `xml:"urn:xmpp:http:upload:0 request"`
	Filename    string   `xml:"filename,attr,omitempty"`
	Size        int64    `xml:"size,attr"`
	ContentType string   `xml:"content-type,attr,omitempty"`
}

func (r *HTTPUploadRequest) Namespace() string {
	return r.XMLName.Space
}

func (r *HTTPUploadRequest) GetSet() *stanza.ResultSet {
	return nil
}

type HTTPUploadSlot struct {
	XMLName xml.Name      `xml:"urn:xmpp:http:upload:0 slot"`
	Put     HTTPUploadPut `xml:"put"`
	Get     HTTPUploadGet `xml:"get"`
}

func (s HTTPUploadSlot) Namespace() string {
	return s.XMLName.Space
}

func (s HTTPUploadSlot) GetSet() *stanza.ResultSet {
	return nil
}

type HTTPUploadPut struct {
	URL     string             `xml:"url,attr"`
	Headers []HTTPUploadHeader `xml:"header"`
}

type HTTPUploadGet struct {
	URL string `xml:"url,attr"`
}

type HTTPUploadHeader struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

func init() {
	stanza.TypeRegistry.MapExtension(
		stanza.PKTIQ,
		xml.Name{Space: NSHTTPUpload, Local: "request"},
		HTTPUploadRequest{},
	)
	stanza.TypeRegistry.MapExtension(
		stanza.PKTIQ,
		xml.Name{Space: NSHTTPUpload, Local: "slot"},
		HTTPUploadSlot{},
	)
}

func (nc *MyNetworkClient) ensureHTTPUploadService(ctx context.Context) (string, error) {
	if nc.httpUploadServiceJID != "" {
		return nc.httpUploadServiceJID, nil
	}
	return nc.discoverHTTPUploadService(ctx)
}

func (nc *MyNetworkClient) discoverHTTPUploadService(ctx context.Context) (string, error) {
	if nc.xmppClient == nil || nc.xmppClient.Session == nil {
		return "", fmt.Errorf("XMPP client not connected")
	}
	serverDomain := nc.serverDomain()
	if serverDomain == "" {
		return "", fmt.Errorf("unable to determine server domain")
	}

	if info, err := nc.requestDiscoInfo(ctx, serverDomain); err == nil {
		if hasDiscoFeature(info, NSHTTPUpload) {
			nc.httpUploadServiceJID = serverDomain
			return serverDomain, nil
		}
	}

	items, err := nc.requestDiscoItems(ctx, serverDomain)
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
		if hasDiscoFeature(info, NSHTTPUpload) {
			nc.httpUploadServiceJID = item.JID
			return item.JID, nil
		}
	}

	return "", fmt.Errorf("no HTTP upload service discovered")
}

func (nc *MyNetworkClient) requestDiscoItems(ctx context.Context, jid string) (*stanza.DiscoItems, error) {
	if nc.xmppClient == nil {
		return nil, fmt.Errorf("XMPP client not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	itemsIQ, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		To:   jid,
		Id:   fmt.Sprintf("disco_items_%d", time.Now().UnixNano()),
	})
	itemsIQ.Payload = &stanza.DiscoItems{}
	ch, err := nc.xmppClient.SendIQ(ctx, itemsIQ)
	if err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			return nil, fmt.Errorf("disco items failed: %s", resp.Type)
		}
		payload, ok := resp.Payload.(*stanza.DiscoItems)
		if !ok || payload == nil {
			return nil, fmt.Errorf("unexpected disco items payload")
		}
		return payload, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (nc *MyNetworkClient) requestDiscoInfo(ctx context.Context, jid string) (*stanza.DiscoInfo, error) {
	if nc.xmppClient == nil {
		return nil, fmt.Errorf("XMPP client not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	infoIQ, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		To:   jid,
		Id:   fmt.Sprintf("disco_info_%d", time.Now().UnixNano()),
	})
	infoIQ.Payload = &stanza.DiscoInfo{}
	ch, err := nc.xmppClient.SendIQ(ctx, infoIQ)
	if err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			return nil, fmt.Errorf("disco info failed: %s", resp.Type)
		}
		payload, ok := resp.Payload.(*stanza.DiscoInfo)
		if !ok || payload == nil {
			return nil, fmt.Errorf("unexpected disco info payload")
		}
		return payload, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func hasDiscoFeature(info *stanza.DiscoInfo, feature string) bool {
	for _, item := range info.Features {
		if item.Var == feature {
			return true
		}
	}
	return false
}

func (nc *MyNetworkClient) requestHTTPUploadSlot(ctx context.Context, serviceJID, filename string, size int64, mimeType string) (*HTTPUploadSlot, error) {
	if nc.xmppClient == nil {
		return nil, fmt.Errorf("XMPP client not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if filename == "" {
		filename = "file"
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	slotIQ, _ := stanza.NewIQ(stanza.Attrs{
		Type: stanza.IQTypeGet,
		To:   serviceJID,
		Id:   fmt.Sprintf("http_upload_%d", time.Now().UnixNano()),
	})
	slotIQ.Payload = &HTTPUploadRequest{
		Filename:    filename,
		Size:        size,
		ContentType: mimeType,
	}
	ch, err := nc.xmppClient.SendIQ(ctx, slotIQ)
	if err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Type != stanza.IQTypeResult {
			return nil, fmt.Errorf("http upload slot failed: %s", resp.Type)
		}
		payload, ok := resp.Payload.(*HTTPUploadSlot)
		if !ok || payload == nil {
			return nil, fmt.Errorf("unexpected http upload slot payload")
		}
		if payload.Put.URL == "" {
			return nil, fmt.Errorf("http upload slot missing put url")
		}
		if payload.Get.URL == "" {
			return nil, fmt.Errorf("http upload slot missing get url")
		}
		return payload, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (nc *MyNetworkClient) putHTTPUpload(ctx context.Context, slot *HTTPUploadSlot, data []byte, mimeType string) error {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, slot.Put.URL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	for _, header := range slot.Put.Headers {
		if header.Name == "" {
			continue
		}
		req.Header.Set(header.Name, header.Value)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", mimeType)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("http upload failed with status %d", resp.StatusCode)
	}
	return nil
}

func (nc *MyNetworkClient) serverDomain() string {
	if nc.xmppClient == nil || nc.xmppClient.Session == nil {
		return ""
	}
	parts := strings.Split(nc.xmppClient.Session.BindJid, "@")
	if len(parts) < 2 {
		return ""
	}
	serverDomain := strings.Split(parts[1], "/")[0]
	return serverDomain
}

func (nc *MyNetworkClient) uploadMatrixMediaToXMPP(ctx context.Context, msg *bridgev2.MatrixMessage) (string, string, error) {
	contentURL := msg.Content.URL
	if contentURL == "" && msg.Content.File != nil {
		contentURL = msg.Content.File.URL
	}
	if contentURL == "" {
		return "", "", fmt.Errorf("media event missing content url")
	}

	botIntent := nc.connector.bridge.Matrix.BotIntent()
	data, err := botIntent.DownloadMedia(ctx, contentURL, msg.Content.File)
	if err != nil {
		return "", "", fmt.Errorf("download media for upload: %w", err)
	}
	if len(data) == 0 {
		return "", "", fmt.Errorf("downloaded media is empty")
	}

	fileName := msg.Content.GetFileName()
	if fileName == "" {
		fileName = "file"
	}
	mimeType := ""
	if msg.Content.Info != nil {
		mimeType = msg.Content.Info.MimeType
	}

	serviceJID, err := nc.ensureHTTPUploadService(ctx)
	if err != nil {
		return "", "", err
	}
	slot, err := nc.requestHTTPUploadSlot(ctx, serviceJID, fileName, int64(len(data)), mimeType)
	if err != nil {
		return "", "", err
	}
	if err := nc.putHTTPUpload(ctx, slot, data, mimeType); err != nil {
		return "", "", err
	}

	url := slot.Get.URL
	if url == "" {
		url = slot.Put.URL
	}

	desc := msg.Content.Body
	if desc == "" {
		desc = fileName
	}
	return url, desc, nil
}
