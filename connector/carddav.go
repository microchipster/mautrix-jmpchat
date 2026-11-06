// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const (
	maxCardDAVAddressbookSize = 32 << 20 // 32 MiB
	cardDAVFetchTimeout       = 60 * time.Second
	cardDAVSyncMaxAdds        = 500
	cardDAVSyncAddInterval    = 200 * time.Millisecond // 5 adds/s
	cardDAVAutoSyncInterval   = 6 * time.Hour
)

// CardDAVConfig stores CardDAV import configuration (network config section).
type CardDAVConfig struct {
	AllowInsecureHTTP  bool   `yaml:"allow_insecure_http,omitempty"`
	AllowPrivateHosts  bool   `yaml:"allow_private_hosts,omitempty"`
	DefaultCountryCode string `yaml:"default_country_code,omitempty"`
	EncryptionKey      string `yaml:"encryption_key,omitempty"`
	KeyFile            string `yaml:"key_file,omitempty"`
}

// CardDAVCredentials stores per-login CardDAV credentials and sync state.
type CardDAVCredentials struct {
	URL           string     `json:"url,omitempty"`
	Username      string     `json:"username,omitempty"`
	Password      string     `json:"password,omitempty"`
	LastSyncAt    *time.Time `json:"last_sync_at,omitempty"`
	LastSyncCount int        `json:"last_sync_count,omitempty"`
}

func (c *MyConnector) cardDAVConfig() CardDAVConfig {
	if c == nil || c.config == nil {
		return CardDAVConfig{DefaultCountryCode: defaultCardDAVCountryCode}
	}
	cfg := c.config.CardDAV
	if strings.TrimSpace(cfg.DefaultCountryCode) == "" {
		cfg.DefaultCountryCode = defaultCardDAVCountryCode
	}
	return cfg
}

func (nc *MyNetworkClient) cardDAVConfig() CardDAVConfig {
	if nc == nil {
		return CardDAVConfig{DefaultCountryCode: defaultCardDAVCountryCode}
	}
	return nc.connector.cardDAVConfig()
}

func (nc *MyNetworkClient) cardDAVCredentials() *CardDAVCredentials {
	if nc == nil || nc.login == nil {
		return nil
	}
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	if meta == nil || meta.CardDAV == nil {
		return nil
	}
	creds := *meta.CardDAV
	if meta.CardDAVKeyMode == cardDAVKeyModeMemory && !cardDAVRAMKeyActive() {
		// The in-memory key is missing (e.g. after a restart): stay silent and
		// report no password instead of spamming warnings.
		creds.Password = ""
		return &creds
	}
	if creds.Password == "" {
		return &creds
	}
	if !cardDAVNameIsEncrypted(creds.Password) {
		// Legacy plaintext password: rewrite it encrypted in the background.
		plain := creds.Password
		go func() {
			migrateCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			store := *meta.CardDAV
			store.Password = plain
			if err := nc.setCardDAVCredentials(migrateCtx, &store); err != nil {
				nc.log.Warn().Err(err).Msg("Failed to migrate the CardDAV password to encrypted storage")
			}
		}()
		return &creds
	}
	decrypted, err := decryptCardDAVValue(nc.cardDAVConfig(), creds.Password)
	if err != nil {
		nc.log.Warn().Err(err).Msg("Failed to decrypt the stored CardDAV password; run carddav-login again")
		decrypted = ""
	}
	creds.Password = decrypted
	return &creds
}

func (nc *MyNetworkClient) setCardDAVCredentials(ctx context.Context, creds *CardDAVCredentials) error {
	if nc == nil || nc.login == nil {
		return fmt.Errorf("login unavailable")
	}
	if creds == nil {
		return fmt.Errorf("CardDAV credentials are missing")
	}
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	if meta == nil {
		meta = &LoginMetadata{}
	}
	stored := *creds
	encrypted, err := encryptCardDAVValue(nc.cardDAVConfig(), stored.Password)
	if err != nil {
		return fmt.Errorf("encrypt CardDAV password: %w (run `carddav-unlock <secret>` to set an in-memory key)", err)
	}
	stored.Password = encrypted
	meta.CardDAV = &stored
	nc.login.Metadata = meta
	return nc.login.Save(ctx)
}

func (nc *MyNetworkClient) clearCardDAVCredentials(ctx context.Context) error {
	if nc == nil || nc.login == nil {
		return fmt.Errorf("login unavailable")
	}
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	if meta == nil || meta.CardDAV == nil {
		return nil
	}
	meta.CardDAV = nil
	nc.login.Metadata = meta
	return nc.login.Save(ctx)
}

// cardDAVLocked reports whether the login uses the in-memory key and the key
// is not present (e.g. after a restart until `carddav-unlock` is run again).
func (nc *MyNetworkClient) cardDAVLocked() bool {
	if nc == nil || nc.login == nil {
		return false
	}
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	return meta != nil && meta.CardDAVKeyMode == cardDAVKeyModeMemory && !cardDAVRAMKeyActive()
}

// cardDAVEncryptionNote describes how stored CardDAV credentials are protected.
func (nc *MyNetworkClient) cardDAVEncryptionNote() string {
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	if meta != nil && meta.CardDAVKeyMode == cardDAVKeyModeMemory {
		return "Credentials are encrypted with your in-memory unlock secret; the key is never written to disk."
	}
	if cardDAVLegacyKeyFileExists() {
		return "Credentials are encrypted at rest with the key file; run `carddav-unlock <secret>` to switch to an in-memory key instead."
	}
	return "Credentials are encrypted at rest."
}

// validateCardDAVURL checks the URL scheme and rejects obviously private
// targets unless the configuration explicitly allows them.
func validateCardDAVURL(cfg CardDAVConfig, raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("empty CardDAV URL")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("invalid CardDAV URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !cfg.AllowInsecureHTTP {
			return fmt.Errorf("plain HTTP is not allowed; use HTTPS or set carddav.allow_insecure_http")
		}
	default:
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("CardDAV URL is missing a host")
	}
	if !cfg.AllowPrivateHosts {
		lower := strings.ToLower(host)
		if lower == "localhost" || strings.HasSuffix(lower, ".local") {
			return fmt.Errorf("private hosts are not allowed; set carddav.allow_private_hosts to override")
		}
		if ip := net.ParseIP(host); ip != nil && isPrivateCardDAVIP(ip) {
			return fmt.Errorf("private host %s is not allowed; set carddav.allow_private_hosts to override", host)
		}
	}
	return nil
}

// isPrivateCardDAVIP reports whether the IP is loopback/private/link-local/
// unspecified or inside the RFC 6598 carrier-grade NAT range.
func isPrivateCardDAVIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	return false
}

// normalizeCardDAVPhoneNumber converts a vCard telephone value to an E.164
// number using the configured default country code when needed.
func normalizeCardDAVPhoneNumber(raw, defaultCountryCode string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	if len(s) >= 4 && strings.EqualFold(s[:4], "tel:") {
		s = strings.TrimSpace(s[4:])
	}
	if strings.ContainsAny(s, ";,") {
		return "", false
	}
	var digits strings.Builder
	seenPlus := false
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			digits.WriteRune(r)
		case r == '+':
			if seenPlus || digits.Len() > 0 {
				return "", false
			}
			seenPlus = true
		case r == ' ' || r == '\t' || r == '-' || r == '(' || r == ')' || r == '.':
			// Formatting characters, ignore.
		default:
			return "", false
		}
	}
	d := digits.String()
	if len(d) < 7 || len(d) > 15 {
		return "", false
	}
	cc := strings.TrimLeft(strings.TrimSpace(defaultCountryCode), "+")
	if seenPlus {
		return "+" + d, true
	}
	if strings.HasPrefix(d, "00") && len(d) > 8 {
		return "+" + d[2:], true
	}
	if len(d) == 10 && cc != "" {
		return "+" + cc + d, true
	}
	if len(d) == 11 && strings.HasPrefix(d, "1") {
		return "+" + d, true
	}
	if len(d) > 10 {
		return "+" + d, true
	}
	return "", false
}

// cardDAVContact is a minimal vCard contact for roster import.
type cardDAVContact struct {
	Name      string
	Phones    []string
	Photo     []byte
	PhotoMime string
}

// parseVCards extracts names, telephone numbers and photos from vCard data.
// It handles line folding and property parameters (including Apple's
// "item1.TEL" style grouping).
func parseVCards(data []byte) []cardDAVContact {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	rawLines := strings.Split(text, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += strings.TrimLeft(line, " \t")
			continue
		}
		lines = append(lines, line)
	}
	if len(data) == 0 {
		return nil
	}
	var contacts []cardDAVContact
	var current *cardDAVContact
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "BEGIN:VCARD") {
			current = &cardDAVContact{}
			continue
		}
		if strings.EqualFold(trimmed, "END:VCARD") {
			if current != nil {
				contacts = append(contacts, *current)
				current = nil
			}
			continue
		}
		if current == nil {
			continue
		}
		name, params, value, ok := splitVCardPropertyParams(trimmed)
		if !ok {
			continue
		}
		if idx := strings.LastIndex(name, "."); idx >= 0 {
			name = name[idx+1:] // Apple "item1.TEL" style properties
		}
		switch strings.ToUpper(name) {
		case "FN":
			if current.Name == "" {
				current.Name = value
			}
		case "TEL":
			if value != "" {
				current.Phones = append(current.Phones, value)
			}
		case "PHOTO":
			if current.Photo == nil {
				if photo, mimeType, ok := parseVCardPhoto(params, value); ok {
					current.Photo = photo
					current.PhotoMime = mimeType
				}
			}
		}
	}
	return contacts
}

// splitVCardPropertyParams splits a content line into property name, raw
// parameters (the part between ';' and ':') and value.
func splitVCardPropertyParams(line string) (name, params, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", "", false
	}
	name = line[:idx]
	value = strings.TrimSpace(line[idx+1:])
	if semi := strings.Index(name, ";"); semi >= 0 {
		params = strings.TrimSpace(name[semi+1:])
		name = name[:semi]
	}
	return strings.TrimSpace(name), params, value, true
}

// parseVCardPhoto decodes a PHOTO property value. Inline base64 payloads and
// data: URIs are supported; URL references are skipped because fetching
// remote photos is deliberately out of scope for the importer.
func parseVCardPhoto(params, value string) ([]byte, string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, "", false
	}
	hint := vCardPhotoMimeHint(params)
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		comma := strings.Index(value, ",")
		if comma < 0 {
			return nil, "", false
		}
		meta := strings.ToLower(value[:comma])
		if !strings.Contains(meta, ";base64") {
			return nil, "", false
		}
		if hint == "" {
			if semi := strings.Index(meta[5:], ";"); semi >= 0 {
				hint = strings.TrimSpace(meta[5 : 5+semi])
			} else {
				hint = strings.TrimSpace(meta[5:])
			}
		}
		decoded, err := base64.StdEncoding.DecodeString(stripASCIIWhitespace(value[comma+1:]))
		if err != nil {
			return nil, "", false
		}
		return validateVCardPhoto(decoded, hint)
	}
	if strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://") {
		return nil, "", false
	}
	if strings.Contains(strings.ToLower(params), "value=uri") {
		return nil, "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(stripASCIIWhitespace(value))
	if err != nil {
		return nil, "", false
	}
	return validateVCardPhoto(decoded, hint)
}

// vCardPhotoMimeHint maps a PHOTO property's TYPE parameter to a MIME type.
func vCardPhotoMimeHint(params string) string {
	for _, param := range strings.Split(params, ";") {
		param = strings.TrimSpace(param)
		if !strings.HasPrefix(strings.ToUpper(param), "TYPE=") {
			continue
		}
		value := strings.Trim(strings.TrimSpace(param[5:]), `"`)
		switch strings.ToUpper(value) {
		case "JPEG", "JPG":
			return "image/jpeg"
		case "PNG":
			return "image/png"
		case "GIF":
			return "image/gif"
		case "WEBP":
			return "image/webp"
		}
		if strings.Contains(value, "/") {
			return strings.ToLower(value)
		}
	}
	return ""
}

// validateVCardPhoto enforces the avatar limits shared with the XMPP avatar
// code paths.
func validateVCardPhoto(data []byte, hint string) ([]byte, string, bool) {
	if len(data) == 0 || len(data) > maxVCardAvatarSize {
		return nil, "", false
	}
	mimeType := hint
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if !allowedVCardAvatarMime(mimeType) {
		return nil, "", false
	}
	return data, mimeType, true
}

// splitVCardProperty splits a content line into its property name and value,
// dropping any property parameters.
func splitVCardProperty(line string) (name, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	name = line[:idx]
	value = strings.TrimSpace(line[idx+1:])
	if semi := strings.Index(name, ";"); semi >= 0 {
		name = name[:semi]
	}
	return strings.TrimSpace(name), value, true
}

// fetchCardDAVAddressbook downloads and size-checks the address book. It first
// tries a plain vCard export and falls back to CardDAV discovery (PROPFIND +
// REPORT) for servers like SOGo that do not serve a combined export.
func fetchCardDAVAddressbook(ctx context.Context, cfg CardDAVConfig, creds CardDAVCredentials) ([]byte, error) {
	if err := validateCardDAVURL(cfg, creds.URL); err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, cardDAVFetchTimeout)
	defer cancel()
	client := &http.Client{
		Timeout: cardDAVFetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	data, plainErr := fetchCardDAVPlain(requestCtx, client, creds)
	if plainErr == nil && looksLikeVCardData(data) {
		return data, nil
	}
	discovered, discoveryErr := fetchCardDAVViaDiscovery(requestCtx, client, creds)
	if discoveryErr == nil && looksLikeVCardData(discovered) {
		return discovered, nil
	}
	switch {
	case plainErr != nil && discoveryErr != nil:
		return nil, fmt.Errorf("fetch CardDAV address book: %v; CardDAV discovery failed: %v", plainErr, discoveryErr)
	case discoveryErr != nil:
		return nil, fmt.Errorf("addressbook response did not contain any vCards; CardDAV discovery failed: %v", discoveryErr)
	case plainErr != nil:
		return nil, plainErr
	default:
		return nil, fmt.Errorf("addressbook response did not contain any vCards")
	}
}

// fetchCardDAVPlain performs a plain GET of the address book URL (the vCard
// export behavior the bridge has always supported).
func fetchCardDAVPlain(ctx context.Context, client *http.Client, creds CardDAVCredentials) ([]byte, error) {
	resp, err := cardDAVDoRequest(ctx, client, http.MethodGet, strings.TrimSpace(creds.URL), "", "", creds)
	if err != nil {
		return nil, fmt.Errorf("fetch CardDAV address book: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("addressbook request failed with status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCardDAVAddressbookSize+1))
	if err != nil {
		return nil, fmt.Errorf("read CardDAV address book: %w", err)
	}
	if len(body) > maxCardDAVAddressbookSize {
		return nil, fmt.Errorf("addressbook is larger than %d bytes", maxCardDAVAddressbookSize)
	}
	return body, nil
}

// looksLikeVCardData reports whether the payload contains at least one vCard.
func looksLikeVCardData(data []byte) bool {
	return strings.Contains(strings.ToUpper(string(data)), "BEGIN:VCARD")
}

// cardDAVDoRequest builds and executes one CardDAV HTTP request. Redirects are
// followed manually (up to four hops, for any method) because some servers
// redirect DAV requests in ways the standard client mishandles.
func cardDAVDoRequest(ctx context.Context, client *http.Client, method, rawURL, depth, body string, creds CardDAVCredentials) (*http.Response, error) {
	current := rawURL
	for redirects := 0; ; redirects++ {
		if redirects > 4 {
			return nil, fmt.Errorf("too many redirects")
		}
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, current, reader)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(creds.Username) != "" {
			req.SetBasicAuth(creds.Username, creds.Password)
		}
		if method == http.MethodGet {
			req.Header.Set("Accept", "text/vcard, text/x-vcard, text/directory")
		} else {
			req.Header.Set("Content-Type", "application/xml; charset=utf-8")
		}
		if depth != "" {
			req.Header.Set("Depth", depth)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 300 || resp.StatusCode > 399 {
			return resp, nil
		}
		location := strings.TrimSpace(resp.Header.Get("Location"))
		if location == "" {
			return resp, nil
		}
		resp.Body.Close()
		next, err := resolveCardDAVRedirect(current, location)
		if err != nil {
			return nil, err
		}
		current = next
	}
}

// resolveCardDAVRedirect resolves a Location header against the current URL.
func resolveCardDAVRedirect(current, location string) (string, error) {
	base, err := url.Parse(current)
	if err != nil {
		return "", fmt.Errorf("invalid CardDAV URL: %w", err)
	}
	target, err := base.Parse(location)
	if err != nil {
		return "", fmt.Errorf("invalid CardDAV redirect: %w", err)
	}
	return target.String(), nil
}

// cardDAVMultiStatus is a parsed DAV multistatus response.
type cardDAVMultiStatus struct {
	Responses []cardDAVMultiStatusResponse `xml:"response"`
}

type cardDAVMultiStatusResponse struct {
	Href     string                       `xml:"href"`
	Propstat []cardDAVMultiStatusPropstat `xml:"propstat"`
}

type cardDAVMultiStatusPropstat struct {
	Prop cardDAVMultiStatusProp `xml:"prop"`
}

type cardDAVMultiStatusProp struct {
	ResourceType cardDAVResourceType `xml:"resourcetype"`
	ContentType  string              `xml:"getcontenttype"`
	AddressData  string              `xml:"address-data"`
}

type cardDAVResourceType struct {
	Addressbook *struct{} `xml:"addressbook"`
}

// cardDAVPropfindBody asks a collection for its resource types and content
// types so the vCard members can be identified.
const cardDAVPropfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">
  <d:prop>
    <d:resourcetype/>
    <d:getcontenttype/>
  </d:prop>
</d:propfind>`

// cardDAVContactGroup is a set of vCard resources served by one collection.
type cardDAVContactGroup struct {
	collectionURL string
	hrefs         []string
}

// fetchCardDAVViaDiscovery walks a CardDAV collection: PROPFIND lists the
// vCard resources (and nested address book collections) and REPORT
// addressbook-multiget downloads them.
func fetchCardDAVViaDiscovery(ctx context.Context, client *http.Client, creds CardDAVCredentials) ([]byte, error) {
	collectionURL := strings.TrimSpace(creds.URL)
	vcards, collections, err := cardDAVDiscoveryHrefs(ctx, client, creds, collectionURL)
	if err != nil {
		return nil, err
	}
	var groups []cardDAVContactGroup
	if len(vcards) > 0 {
		groups = append(groups, cardDAVContactGroup{collectionURL: collectionURL, hrefs: vcards})
	}
	for _, collection := range collections {
		if len(groups) >= 10 {
			break
		}
		nested, _, err := cardDAVDiscoveryHrefs(ctx, client, creds, collection)
		if err != nil || len(nested) == 0 {
			continue
		}
		groups = append(groups, cardDAVContactGroup{collectionURL: collection, hrefs: nested})
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("no contacts were discovered in the address book")
	}
	var out strings.Builder
	for _, group := range groups {
		data, err := cardDAVMultiget(ctx, client, creds, group.collectionURL, group.hrefs)
		if err != nil || !looksLikeVCardData(data) {
			data, err = cardDAVIndividualGets(ctx, client, creds, group.hrefs)
			if err != nil {
				return nil, err
			}
		}
		if len(data) == 0 {
			continue
		}
		out.Write(data)
		out.WriteString("\r\n")
	}
	return []byte(out.String()), nil
}

// cardDAVDiscoveryHrefs lists the vCard resources and nested address book
// collections inside a CardDAV collection.
func cardDAVDiscoveryHrefs(ctx context.Context, client *http.Client, creds CardDAVCredentials, collectionURL string) ([]string, []string, error) {
	base, err := url.Parse(collectionURL)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid CardDAV URL: %w", err)
	}
	resp, err := cardDAVDoRequest(ctx, client, "PROPFIND", collectionURL, "1", cardDAVPropfindBody, creds)
	if err != nil {
		return nil, nil, fmt.Errorf("CardDAV PROPFIND: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, nil, fmt.Errorf("CardDAV PROPFIND failed with status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCardDAVAddressbookSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read CardDAV PROPFIND response: %w", err)
	}
	if len(body) > maxCardDAVAddressbookSize {
		return nil, nil, fmt.Errorf("CardDAV PROPFIND response is larger than %d bytes", maxCardDAVAddressbookSize)
	}
	var multiStatus cardDAVMultiStatus
	if err := xml.Unmarshal(body, &multiStatus); err != nil {
		return nil, nil, fmt.Errorf("parse CardDAV PROPFIND response: %w", err)
	}
	self := strings.TrimRight(base.Path, "/")
	vcards := make([]string, 0, len(multiStatus.Responses))
	collections := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, item := range multiStatus.Responses {
		href := strings.TrimSpace(item.Href)
		if href == "" {
			continue
		}
		resolved, err := base.Parse(href)
		if err != nil || resolved.Path == "" {
			continue
		}
		resolved.Fragment = ""
		if strings.TrimRight(resolved.Path, "/") == self {
			continue // the collection itself
		}
		key := resolved.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		isCollection := false
		isVCard := strings.HasSuffix(strings.ToLower(resolved.Path), ".vcf")
		for _, propstat := range item.Propstat {
			if propstat.Prop.ResourceType.Addressbook != nil {
				isCollection = true
				break
			}
			if strings.Contains(strings.ToLower(propstat.Prop.ContentType), "vcard") {
				isVCard = true
			}
		}
		if isCollection {
			if len(collections) < 10 {
				collections = append(collections, key)
			}
			continue
		}
		if !isVCard || len(vcards) >= 2000 {
			continue
		}
		vcards = append(vcards, key)
	}
	return vcards, collections, nil
}

// cardDAVMultiget downloads the listed contacts with a REPORT
// addressbook-multiget, falling back to individual GETs when the server does
// not answer with address data.
func cardDAVMultiget(ctx context.Context, client *http.Client, creds CardDAVCredentials, collectionURL string, hrefs []string) ([]byte, error) {
	const batchSize = 200
	var vcards strings.Builder
	for start := 0; start < len(hrefs); start += batchSize {
		end := start + batchSize
		if end > len(hrefs) {
			end = len(hrefs)
		}
		data, err := cardDAVMultigetBatch(ctx, client, creds, collectionURL, hrefs[start:end])
		if err != nil || data == "" {
			return cardDAVIndividualGets(ctx, client, creds, hrefs)
		}
		vcards.WriteString(data)
	}
	return []byte(vcards.String()), nil
}

// cardDAVMultigetBatch fetches one batch of contacts via REPORT.
func cardDAVMultigetBatch(ctx context.Context, client *http.Client, creds CardDAVCredentials, collectionURL string, hrefs []string) (string, error) {
	var body strings.Builder
	body.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n")
	body.WriteString("<c:addressbook-multiget xmlns:d=\"DAV:\" xmlns:c=\"urn:ietf:params:xml:ns:carddav\">\n")
	body.WriteString("  <d:prop><d:getetag/><c:address-data/></d:prop>\n")
	for _, href := range hrefs {
		body.WriteString("  <d:href>")
		if err := xml.EscapeText(&body, []byte(href)); err != nil {
			return "", fmt.Errorf("build CardDAV REPORT request: %w", err)
		}
		body.WriteString("</d:href>\n")
	}
	body.WriteString("</c:addressbook-multiget>\n")
	resp, err := cardDAVDoRequest(ctx, client, "REPORT", collectionURL, "1", body.String(), creds)
	if err != nil {
		return "", fmt.Errorf("CardDAV REPORT: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		return "", fmt.Errorf("CardDAV REPORT failed with status %s", resp.Status)
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxCardDAVAddressbookSize+1))
	if err != nil {
		return "", fmt.Errorf("read CardDAV REPORT response: %w", err)
	}
	if len(responseBody) > maxCardDAVAddressbookSize {
		return "", fmt.Errorf("CardDAV REPORT response is larger than %d bytes", maxCardDAVAddressbookSize)
	}
	var multiStatus cardDAVMultiStatus
	if err := xml.Unmarshal(responseBody, &multiStatus); err != nil {
		return "", fmt.Errorf("parse CardDAV REPORT response: %w", err)
	}
	var out strings.Builder
	for _, item := range multiStatus.Responses {
		for _, propstat := range item.Propstat {
			data := strings.TrimSpace(propstat.Prop.AddressData)
			if data == "" {
				continue
			}
			out.WriteString(data)
			out.WriteString("\r\n")
		}
	}
	return out.String(), nil
}

// cardDAVIndividualGets fetches contacts one by one when REPORT address data
// is unavailable.
func cardDAVIndividualGets(ctx context.Context, client *http.Client, creds CardDAVCredentials, hrefs []string) ([]byte, error) {
	const maxContacts = 1000
	var out strings.Builder
	size := 0
	for index, href := range hrefs {
		if index >= maxContacts || size >= maxCardDAVAddressbookSize {
			break
		}
		resp, err := cardDAVDoRequest(ctx, client, http.MethodGet, href, "", "", creds)
		if err != nil {
			return nil, fmt.Errorf("fetch CardDAV contact: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxCardDAVAddressbookSize-size)+1))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read CardDAV contact: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			continue
		}
		if !looksLikeVCardData(body) {
			continue
		}
		if len(body) > maxCardDAVAddressbookSize-size {
			break
		}
		out.Write(body)
		out.WriteString("\r\n")
		size += len(body)
	}
	return []byte(out.String()), nil
}

// cardDAVSyncResult summarizes one CardDAV import run.
type cardDAVSyncResult struct {
	Contacts   int
	Numbers    int
	Candidates int
	Added      int
	Updated    int
	Skipped    int
	Conflicts  int
	Photos     int
	DryRun     bool
}

// syncCardDAV fetches the configured addressbook and (when apply is true)
// adds missing phone numbers to the XMPP roster as +E.164@cheogram.com
// entries. Without apply it only reports what would be added.
func (nc *MyNetworkClient) syncCardDAV(ctx context.Context, apply bool) (cardDAVSyncResult, error) {
	result := cardDAVSyncResult{DryRun: !apply}
	creds := nc.cardDAVCredentials()
	if creds == nil || strings.TrimSpace(creds.URL) == "" {
		return result, fmt.Errorf("CardDAV is not configured. Run: carddav-login <addressbook URL> <username> <password>")
	}
	if nc.cardDAVLocked() {
		return result, fmt.Errorf("CardDAV is locked: run `carddav-unlock <secret>` in the management room to restore the in-memory key")
	}
	if apply && !nc.isXMPPConnected() {
		return result, fmt.Errorf("not connected to the XMPP server")
	}
	cfg := nc.cardDAVConfig()
	data, err := fetchCardDAVAddressbook(ctx, cfg, *creds)
	if err != nil {
		return result, err
	}
	contacts := parseVCards(data)
	existing := map[string]bool{}
	for _, jid := range nc.rosterContactList() {
		existing[jid] = true
	}
	seen := map[string]bool{}
	for _, contact := range contacts {
		result.Contacts++
		var contactJIDs []string
		for _, raw := range contact.Phones {
			number, ok := normalizeCardDAVPhoneNumber(raw, cfg.DefaultCountryCode)
			if !ok {
				continue
			}
			result.Numbers++
			jid := number + "@cheogram.com"
			if seen[jid] {
				continue
			}
			seen[jid] = true
			contactJIDs = append(contactJIDs, jid)
			if existing[jid] && apply {
				if meta := nc.cardDAVGhostMeta(ctx, jid); meta != nil && strings.TrimSpace(meta.CardDAVName) != "" && (nc.cardDAVStoredName(meta) != contact.Name || !cardDAVNameIsEncrypted(meta.CardDAVName)) {
					if conflict := nc.setGhostCardDAVName(ctx, jid, contact.Name); conflict {
						result.Conflicts++
					}
					result.Updated++
				}
			}
			if existing[jid] {
				result.Skipped++
				continue
			}
			result.Candidates++
			if !apply || result.Added >= cardDAVSyncMaxAdds {
				continue
			}
			if err := nc.setRosterItemCore(ctx, jid, contact.Name); err != nil {
				nc.log.Warn().Err(err).Str("jid", jid).Msg("Failed to add CardDAV contact to roster")
				continue
			}
			if conflict := nc.setGhostCardDAVName(ctx, jid, contact.Name); conflict {
				result.Conflicts++
				nc.log.Debug().
					Str("jid", jid).
					Str("carddav_name", contact.Name).
					Msg("CardDAV name stored but an explicit set-contact-name entry takes precedence")
			}
			existing[jid] = true
			result.Added++
			time.Sleep(cardDAVSyncAddInterval)
		}
		if apply && len(contact.Photo) > 0 && len(contactJIDs) > 0 {
			result.Photos += nc.applyCardDAVPhoto(ctx, contactJIDs, contact.Photo)
		}
	}
	if apply {
		now := time.Now()
		creds.LastSyncAt = &now
		creds.LastSyncCount = result.Added
		if err := nc.setCardDAVCredentials(ctx, creds); err != nil {
			nc.log.Warn().Err(err).Msg("Failed to persist CardDAV sync state")
		}
		nc.refreshRosterNamedPortals()
	}
	return result, nil
}

// applyCardDAVPhoto stores a CardDAV photo on existing ghosts for the given
// contact JIDs and refreshes their DM portal avatars. Live XMPP avatars win:
// ghosts with a fetched XMPP avatar are left untouched.
func (nc *MyNetworkClient) applyCardDAVPhoto(ctx context.Context, jids []string, photo []byte) int {
	if nc.bridge == nil || len(photo) == 0 {
		return 0
	}
	sum := sha256.Sum256(photo)
	hash := fmt.Sprintf("%x", sum[:8])
	applied := 0
	for _, jid := range jids {
		ghost, err := nc.bridge.GetExistingGhostByID(ctx, MakeUserID(jid))
		if err != nil {
			nc.log.Warn().Err(err).Str("jid", jid).Msg("Failed to load ghost for CardDAV photo")
			continue
		}
		if ghost == nil {
			continue
		}
		meta, _ := ghost.Metadata.(*GhostMetadata)
		if meta == nil {
			meta = &GhostMetadata{}
		}
		if strings.TrimSpace(meta.AvatarHash) != "" {
			continue
		}
		if meta.CardDAVPhotoHash == hash && ghost.AvatarMXC != "" {
			continue
		}
		meta.CardDAVPhotoHash = hash
		ghost.Metadata = meta
		photoCopy := photo
		ghost.UpdateInfo(ctx, &bridgev2.UserInfo{Avatar: &bridgev2.Avatar{
			ID:  networkid.AvatarID("carddav:" + hash),
			Get: func(ctx context.Context) ([]byte, error) { return photoCopy, nil },
		}})
		if err := nc.bridge.DB.Ghost.Update(ctx, ghost.Ghost); err != nil {
			nc.log.Warn().Err(err).Str("jid", jid).Msg("Failed to store CardDAV photo hash")
			continue
		}
		nc.refreshContactPortalAvatars(ctx, jid, &bridgev2.Avatar{
			ID:   ghost.AvatarID,
			MXC:  ghost.AvatarMXC,
			Hash: ghost.AvatarHash,
		})
		applied++
	}
	return applied
}

// cardDAVGhostMeta loads the ghost metadata of a CardDAV contact JID.
func (nc *MyNetworkClient) cardDAVGhostMeta(ctx context.Context, jid string) *GhostMetadata {
	if nc == nil || nc.bridge == nil || nc.bridge.DB == nil {
		return nil
	}
	canonical := CanonicalBareJID(jid)
	if canonical == "" {
		return nil
	}
	ghost, err := nc.bridge.GetExistingGhostByID(ctx, MakeUserID(canonical))
	if err != nil || ghost == nil {
		return nil
	}
	meta, _ := ghost.Metadata.(*GhostMetadata)
	return meta
}

// startCardDAVAutoSync launches the background address book refresh loop once.
func (nc *MyNetworkClient) startCardDAVAutoSync() {
	nc.autoSyncMu.Lock()
	defer nc.autoSyncMu.Unlock()
	if nc.autoSyncStop != nil {
		return
	}
	stop := make(chan struct{})
	nc.autoSyncStop = stop
	go nc.cardDAVAutoSyncLoop(stop)
}

// stopCardDAVAutoSync stops the background address book refresh loop.
func (nc *MyNetworkClient) stopCardDAVAutoSync() {
	nc.autoSyncMu.Lock()
	stop := nc.autoSyncStop
	nc.autoSyncStop = nil
	nc.autoSyncMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// cardDAVAutoSyncLoop refreshes the address book import in the background so
// contact names added anywhere (for example in a phone's contacts app synced
// to CardDAV) reach the auto-named rooms without a manual `carddav-sync`.
func (nc *MyNetworkClient) cardDAVAutoSyncLoop(stopCh <-chan struct{}) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-timer.C:
		}
		if creds := nc.cardDAVCredentials(); creds != nil && !nc.cardDAVLocked() && strings.TrimSpace(creds.URL) != "" && nc.isXMPPConnected() {
			result, err := nc.syncCardDAV(context.Background(), true)
			if err != nil {
				nc.log.Warn().Err(err).Msg("Background CardDAV sync failed")
			} else if result.Added > 0 || result.Updated > 0 || result.Photos > 0 {
				if changed, err := refreshAutoNamedPortals(context.Background(), nc.bridge, nc.login, nc.contactNameSuffix()); err != nil {
					nc.log.Warn().Err(err).Msg("Background room name refresh failed")
				} else if changed > 0 {
					nc.log.Info().Int("rooms", changed).Msg("Renamed auto-named rooms after a background CardDAV sync")
				}
			}
		}
		timer.Reset(cardDAVAutoSyncInterval)
	}
}
