// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
)

// CardDAVConfig stores CardDAV import configuration (network config section).
type CardDAVConfig struct {
	AllowInsecureHTTP  bool   `yaml:"allow_insecure_http,omitempty"`
	AllowPrivateHosts  bool   `yaml:"allow_private_hosts,omitempty"`
	DefaultCountryCode string `yaml:"default_country_code,omitempty"`
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
	if meta == nil {
		return nil
	}
	return meta.CardDAV
}

func (nc *MyNetworkClient) setCardDAVCredentials(ctx context.Context, creds *CardDAVCredentials) error {
	if nc == nil || nc.login == nil {
		return fmt.Errorf("login unavailable")
	}
	meta, _ := nc.login.Metadata.(*LoginMetadata)
	if meta == nil {
		meta = &LoginMetadata{}
	}
	meta.CardDAV = creds
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

// fetchCardDAVAddressbook downloads the addressbook export referenced by the
// stored credentials.
func fetchCardDAVAddressbook(ctx context.Context, cfg CardDAVConfig, creds CardDAVCredentials) ([]byte, error) {
	if err := validateCardDAVURL(cfg, creds.URL); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, cardDAVFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(creds.URL), nil)
	if err != nil {
		return nil, err
	}
	if creds.Username != "" {
		req.SetBasicAuth(creds.Username, creds.Password)
	}
	req.Header.Set("Accept", "text/vcard, text/x-vcard, text/directory")
	client := &http.Client{Timeout: cardDAVFetchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("addressbook request failed with status %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCardDAVAddressbookSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCardDAVAddressbookSize {
		return nil, fmt.Errorf("addressbook response is larger than %d MiB", maxCardDAVAddressbookSize>>20)
	}
	return data, nil
}

// cardDAVSyncResult summarizes one CardDAV import run.
type cardDAVSyncResult struct {
	Contacts   int
	Numbers    int
	Candidates int
	Added      int
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
