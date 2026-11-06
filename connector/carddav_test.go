// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeCardDAVPhoneNumber(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		country string
		want    string
		wantOK  bool
	}{
		{name: "formatted with plus", raw: "+1 (555) 010-1100", country: "1", want: "+15550101100", wantOK: true},
		{name: "local ten digits", raw: "5550101100", country: "1", want: "+15550101100", wantOK: true},
		{name: "eleven digits with country code", raw: "15550101100", country: "1", want: "+15550101100", wantOK: true},
		{name: "international 00 prefix", raw: "00447700900123", country: "1", want: "+447700900123", wantOK: true},
		{name: "tel URI", raw: "tel:+15550101100", country: "1", want: "+15550101100", wantOK: true},
		{name: "dashes and dots", raw: "555.010.1100", country: "1", want: "+15550101100", wantOK: true},
		{name: "short code rejected", raw: "69061", country: "1", want: "", wantOK: false},
		{name: "extension rejected", raw: "+1 555 010 1100 ext 5", country: "1", want: "", wantOK: false},
		{name: "letters rejected", raw: "not-a-number", country: "1", want: "", wantOK: false},
		{name: "empty rejected", raw: "  ", country: "1", want: "", wantOK: false},
		{name: "plus in middle rejected", raw: "416+9938000", country: "1", want: "", wantOK: false},
		{name: "too long rejected", raw: "+1234567890123456789", country: "1", want: "", wantOK: false},
		{name: "no country code still handles plus", raw: "+15550101100", country: "", want: "+15550101100", wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := normalizeCardDAVPhoneNumber(tc.raw, tc.country)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("normalizeCardDAVPhoneNumber(%q, %q) = (%q, %t), want (%q, %t)", tc.raw, tc.country, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestParseVCards(t *testing.T) {
	t.Parallel()
	raw := "BEGIN:VCARD\r\n" +
		"VERSION:3.0\r\n" +
		"FN:O'Brien & Sons\r\n" +
		"TEL;TYPE=CELL:+15550101100\r\n" +
		"TEL;TYPE=HOME:416-555-0100\r\n" +
		"item1.TEL:tel:+15550101200\r\n" +
		"item1.X-ABLabel:_$!<Other>!$_\r\n" +
		"END:VCARD\r\n" +
		"BEGIN:VCARD\r\n" +
		"VERSION:3.0\r\n" +
		"FN:Jos\\, Jr\r\n" +
		"FN:Folded\r\n" +
		"TEL:tel:+15550102100\r\n" +
		"PHOTO;ENCODING=b;TYPE=JPEG:aGVsbG8=\r\n" +
		"END:VCARD\r\n"
	got := parseVCards([]byte(raw))
	want := []cardDAVContact{
		{Name: "O'Brien & Sons", Phones: []string{"+15550101100", "416-555-0100", "tel:+15550101200"}},
		{Name: "Jos\\, Jr", Phones: []string{"tel:+15550102100"}, Photo: []byte("hello"), PhotoMime: "image/jpeg"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseVCards() = %#v, want %#v", got, want)
	}
}

func TestParseVCardsFoldedName(t *testing.T) {
	t.Parallel()
	raw := "BEGIN:VCARD\nFN:Mary\n Jane\nTEL:5551234567\nEND:VCARD\n"
	got := parseVCards([]byte(raw))
	if len(got) != 1 {
		t.Fatalf("expected one contact, got %d", len(got))
	}
	if got[0].Name != "MaryJane" {
		t.Errorf("folded name = %q, want %q", got[0].Name, "MaryJane")
	}
}

func TestSplitVCardProperty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		line      string
		wantName  string
		wantValue string
		wantOK    bool
	}{
		{name: "with parameters", line: "TEL;TYPE=CELL:+123", wantName: "TEL", wantValue: "+123", wantOK: true},
		{name: "plain", line: "FN:O'Brien", wantName: "FN", wantValue: "O'Brien", wantOK: true},
		{name: "apple grouping", line: "item1.TEL:tel:+44", wantName: "item1.TEL", wantValue: "tel:+44", wantOK: true},
		{name: "no colon", line: "BROKEN", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name, value, ok := splitVCardProperty(tc.line)
			if name != tc.wantName || value != tc.wantValue || ok != tc.wantOK {
				t.Errorf("splitVCardProperty(%q) = (%q, %q, %t), want (%q, %q, %t)", tc.line, name, value, ok, tc.wantName, tc.wantValue, tc.wantOK)
			}
		})
	}
}

func TestValidateCardDAVURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     CardDAVConfig
		raw     string
		wantErr bool
	}{
		{name: "https ok", cfg: CardDAVConfig{}, raw: "https://example.com/addressbook.vcf"},
		{name: "http denied", cfg: CardDAVConfig{}, raw: "http://example.com/addressbook.vcf", wantErr: true},
		{name: "http allowed", cfg: CardDAVConfig{AllowInsecureHTTP: true}, raw: "http://example.com/addressbook.vcf"},
		{name: "loopback denied", cfg: CardDAVConfig{}, raw: "https://127.0.0.1/addressbook.vcf", wantErr: true},
		{name: "loopback allowed", cfg: CardDAVConfig{AllowPrivateHosts: true}, raw: "https://127.0.0.1/addressbook.vcf"},
		{name: "private range denied", cfg: CardDAVConfig{}, raw: "https://192.168.1.10/addressbook.vcf", wantErr: true},
		{name: "cgnat denied", cfg: CardDAVConfig{}, raw: "https://100.64.0.1/addressbook.vcf", wantErr: true},
		{name: "localhost denied", cfg: CardDAVConfig{}, raw: "https://localhost/addressbook.vcf", wantErr: true},
		{name: "ftp denied", cfg: CardDAVConfig{}, raw: "ftp://example.com/addressbook.vcf", wantErr: true},
		{name: "empty denied", cfg: CardDAVConfig{}, raw: "  ", wantErr: true},
		{name: "no host denied", cfg: CardDAVConfig{}, raw: "https:///addressbook.vcf", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateCardDAVURL(tc.cfg, tc.raw)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateCardDAVURL(%+v, %q) error = %v, wantErr %t", tc.cfg, tc.raw, err, tc.wantErr)
			}
		})
	}
}

func TestIsPrivateCardDAVIP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ip   string
		want bool
	}{
		{ip: "127.0.0.1", want: true},
		{ip: "10.0.0.1", want: true},
		{ip: "172.16.5.4", want: true},
		{ip: "192.168.0.1", want: true},
		{ip: "169.254.1.1", want: true},
		{ip: "100.64.0.1", want: true},
		{ip: "100.127.255.254", want: true},
		{ip: "100.128.0.1", want: false},
		{ip: "8.8.8.8", want: false},
		{ip: "::1", want: true},
		{ip: "fe80::1", want: true},
		{ip: "2001:db8::1", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.ip, func(t *testing.T) {
			t.Parallel()
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("failed to parse test IP %q", tc.ip)
			}
			if got := isPrivateCardDAVIP(ip); got != tc.want {
				t.Errorf("isPrivateCardDAVIP(%s) = %t, want %t", tc.ip, got, tc.want)
			}
		})
	}
}

func TestParseCardDAVSyncArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args  []string
		apply bool
		force bool
		ok    bool
	}{
		{nil, true, false, true},
		{[]string{}, true, false, true},
		{[]string{"apply"}, true, false, true},
		{[]string{"--apply"}, true, false, true},
		{[]string{"dry"}, false, false, true},
		{[]string{"dry-run"}, false, false, true},
		{[]string{"--dry-run"}, false, false, true},
		{[]string{"--no-apply"}, false, false, true},
		{[]string{"--force-names"}, true, true, true},
		{[]string{"--no-apply", "--force-names"}, false, true, true},
		{[]string{"APPLY", "Force-Names"}, true, true, true},
		{[]string{"--bogus"}, false, false, false},
		{[]string{"apply", "extra"}, false, false, false},
	}
	for _, tc := range cases {
		apply, force, ok := parseCardDAVSyncArgs(tc.args)
		if apply != tc.apply || force != tc.force || ok != tc.ok {
			t.Errorf("parseCardDAVSyncArgs(%v) = (%v, %v, %v), want (%v, %v, %v)", tc.args, apply, force, ok, tc.apply, tc.force, tc.ok)
		}
	}
}

func TestParseVCardPhoto(t *testing.T) {
	t.Parallel()
	jpegB64 := base64.StdEncoding.EncodeToString([]byte{0xFF, 0xD8, 0xFF, 0xE0, 'j', 'p', 'g'})
	pngData := append([]byte("\x89PNG\r\n\x1a\n"), []byte("imagedata")...)
	pngB64 := base64.StdEncoding.EncodeToString(pngData)
	cases := map[string]struct {
		params   string
		value    string
		wantMime string
		wantLen  int
		wantOK   bool
	}{
		"base64 with type":  {params: "TYPE=JPEG;ENCODING=b", value: jpegB64, wantMime: "image/jpeg", wantLen: 7, wantOK: true},
		"type detected":     {params: "", value: pngB64, wantMime: "image/png", wantLen: len(pngData), wantOK: true},
		"data URI":          {params: "", value: "data:image/png;base64," + pngB64, wantMime: "image/png", wantLen: len(pngData), wantOK: true},
		"URL skipped":       {params: "VALUE=uri", value: "https://example.com/a.jpg", wantOK: false},
		"other URI skipped": {params: "VALUE=uri", value: "cid:abc", wantOK: false},
		"invalid base64":    {params: "TYPE=JPEG", value: "not base64!!!", wantOK: false},
		"empty":             {params: "TYPE=JPEG", value: "", wantOK: false},
		"unsupported mime":  {params: "TYPE=application/pdf", value: base64.StdEncoding.EncodeToString([]byte("%PDF-1.4")), wantOK: false},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			photo, mimeType, ok := parseVCardPhoto(tc.params, tc.value)
			if ok != tc.wantOK {
				t.Fatalf("parseVCardPhoto(%q, %q) ok = %v, want %v", tc.params, tc.value, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if mimeType != tc.wantMime {
				t.Fatalf("parseVCardPhoto(%q, %q) mime = %q, want %q", tc.params, tc.value, mimeType, tc.wantMime)
			}
			if len(photo) != tc.wantLen {
				t.Fatalf("parseVCardPhoto(%q, %q) len = %d, want %d", tc.params, tc.value, len(photo), tc.wantLen)
			}
		})
	}
}

func TestParseVCardsWithPhoto(t *testing.T) {
	t.Parallel()
	jpegB64 := base64.StdEncoding.EncodeToString([]byte{0xFF, 0xD8, 0xFF, 0xE0, 'j', 'p', 'g'})
	raw := "BEGIN:VCARD\r\nFN:Alice\r\nTEL:+15551234567\r\nPHOTO;TYPE=JPEG;ENCODING=b:" + jpegB64 + "\r\nEND:VCARD\r\n"
	contacts := parseVCards([]byte(raw))
	if len(contacts) != 1 {
		t.Fatalf("parseVCards returned %d contacts, want 1", len(contacts))
	}
	if contacts[0].Name != "Alice" || len(contacts[0].Phones) != 1 {
		t.Fatalf("parseVCards contact = %+v", contacts[0])
	}
	if len(contacts[0].Photo) != 7 || contacts[0].PhotoMime != "image/jpeg" {
		t.Fatalf("parseVCards photo = %d bytes, mime %q", len(contacts[0].Photo), contacts[0].PhotoMime)
	}
}

func TestFetchCardDAVAddressbookDAVDiscovery(t *testing.T) {
	const aliceVCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Alice Example\r\nTEL:+15551230001\r\nEND:VCARD\r\n"
	const bobVCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob Example\r\nTEL:+15551230002\r\nEND:VCARD\r\n"

	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if strings.HasSuffix(r.URL.Path, ".vcf") {
				w.Header().Set("Content-Type", "text/vcard")
				fmt.Fprint(w, aliceVCard, bobVCard)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body>not a vcard export</body></html>")
		case "PROPFIND":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">
  <d:response><d:href>/dav/</d:href><d:propstat><d:prop><d:resourcetype><c:addressbook/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
  <d:response><d:href>/dav/alice.vcf</d:href><d:propstat><d:prop><d:getcontenttype>text/vcard</d:getcontenttype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
  <d:response><d:href>/dav/bob.vcf</d:href><d:propstat><d:prop><d:getcontenttype>text/vcard</d:getcontenttype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`)
		case "REPORT":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">
  <d:response><d:href>/dav/alice.vcf</d:href><d:propstat><d:prop><c:address-data>%s</c:address-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
  <d:response><d:href>/dav/bob.vcf</d:href><d:propstat><d:prop><c:address-data>%s</c:address-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`, aliceVCard, bobVCard)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	cfg := CardDAVConfig{AllowInsecureHTTP: true, AllowPrivateHosts: true, DefaultCountryCode: "1"}
	creds := CardDAVCredentials{URL: server.URL + "/dav/", Username: "alice", Password: "secret"}
	data, err := fetchCardDAVAddressbook(context.Background(), cfg, creds)
	if err != nil {
		t.Fatalf("fetchCardDAVAddressbook: %v", err)
	}
	contacts := parseVCards(data)
	if len(contacts) != 2 {
		t.Fatalf("expected 2 contacts, got %d", len(contacts))
	}
	if contacts[0].Name != "Alice Example" || contacts[1].Name != "Bob Example" {
		t.Fatalf("unexpected contact names: %q, %q", contacts[0].Name, contacts[1].Name)
	}
}

func TestFetchCardDAVAddressbookDAVFallback(t *testing.T) {
	const carolVCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Carol Example\r\nTEL:+15551230003\r\nEND:VCARD\r\n"

	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if strings.HasSuffix(r.URL.Path, ".vcf") {
				w.Header().Set("Content-Type", "text/vcard")
				fmt.Fprint(w, carolVCard)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body>not a vcard export</body></html>")
		case "PROPFIND":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">
  <d:response><d:href>/dav/carol.vcf</d:href><d:propstat><d:prop><d:getcontenttype>text/vcard</d:getcontenttype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`)
		case "REPORT":
			w.WriteHeader(http.StatusNotImplemented)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	cfg := CardDAVConfig{AllowInsecureHTTP: true, AllowPrivateHosts: true, DefaultCountryCode: "1"}
	creds := CardDAVCredentials{URL: server.URL + "/dav/", Username: "alice", Password: "secret"}
	data, err := fetchCardDAVAddressbook(context.Background(), cfg, creds)
	if err != nil {
		t.Fatalf("fetchCardDAVAddressbook: %v", err)
	}
	contacts := parseVCards(data)
	if len(contacts) != 1 || contacts[0].Name != "Carol Example" {
		t.Fatalf("expected Carol Example contact, got %#v", contacts)
	}
}

func TestCardDAVCandidateURLs(t *testing.T) {
	candidates := cardDAVCandidateURLs("alice@example.com", "alice@example.com")
	if len(candidates) == 0 {
		t.Fatal("expected candidate URLs for an email address")
	}
	joined := strings.Join(candidates, "\n")
	for _, want := range []string{".well-known/carddav", "/SOGo/dav/", "remote.php/dav/addressbooks", "carddav.fastmail.com"} {
		if !strings.Contains(joined, want) {
			t.Errorf("candidate list is missing %q:\n%s", want, joined)
		}
	}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if !strings.HasPrefix(candidate, "https://") {
			t.Errorf("candidate %q is not an https URL", candidate)
		}
		if _, ok := seen[candidate]; ok {
			t.Errorf("candidate %q is duplicated", candidate)
		}
		seen[candidate] = struct{}{}
	}
	fastmail := cardDAVCandidateURLs("bob@fastmail.com", "bob@fastmail.com")
	if len(fastmail) == 0 || !strings.HasPrefix(fastmail[0], "https://carddav.fastmail.com/") {
		t.Errorf("expected the Fastmail endpoint first, got %v", fastmail)
	}
	if got := cardDAVCandidateURLs("", ""); len(got) != 0 {
		t.Errorf("expected no candidates for an empty hint, got %v", got)
	}
	if got := cardDAVCandidateURLs("https://example.com/dav/", ""); len(got) != 0 {
		t.Errorf("expected no candidates for a URL hint, got %v", got)
	}
}

func TestDiscoverCardDAVAddressbook(t *testing.T) {
	cfg := CardDAVConfig{AllowInsecureHTTP: true, AllowPrivateHosts: true, DefaultCountryCode: "1"}
	candidates := cardDAVCandidateURLs("alice@example.com", "alice@example.com")
	if len(candidates) < 2 {
		t.Fatalf("expected at least two candidates, got %d", len(candidates))
	}
	fetcher := func(ctx context.Context, cfg CardDAVConfig, creds CardDAVCredentials) ([]byte, error) {
		if creds.URL != candidates[1] {
			return nil, fmt.Errorf("not this one")
		}
		return []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Alice Example\r\nEND:VCARD\r\n"), nil
	}
	found, data, tried, err := discoverCardDAVAddressbookWith(context.Background(), cfg, candidates, "alice@example.com", "secret", fetcher)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if found != candidates[1] {
		t.Errorf("expected %q, got %q", candidates[1], found)
	}
	if !looksLikeVCardData(data) {
		t.Errorf("expected vCard data, got %q", data)
	}
	if tried != 2 {
		t.Errorf("expected 2 attempts, got %d", tried)
	}
	failing := func(ctx context.Context, cfg CardDAVConfig, creds CardDAVCredentials) ([]byte, error) {
		return nil, fmt.Errorf("nope")
	}
	if _, _, tried, err := discoverCardDAVAddressbookWith(context.Background(), cfg, candidates, "alice@example.com", "secret", failing); err == nil {
		t.Error("expected an error when every candidate fails")
	} else if tried != len(candidates) {
		t.Errorf("expected %d attempts, got %d", len(candidates), tried)
	}
}

func TestCardDAVEncryptionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "carddav.key")
	cfg := CardDAVConfig{KeyFile: keyFile}
	encrypted, err := encryptCardDAVValue(cfg, "s3cret")
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if !strings.HasPrefix(encrypted, "enc:v1:") {
		t.Fatalf("expected the encrypted prefix, got %q", encrypted)
	}
	if encrypted == "s3cret" {
		t.Fatal("the encrypted value must differ from the plaintext")
	}
	if !cardDAVNameIsEncrypted(encrypted) {
		t.Error("expected the encrypted value to be recognized")
	}
	decrypted, err := decryptCardDAVValue(cfg, encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if decrypted != "s3cret" {
		t.Errorf("expected %q, got %q", "s3cret", decrypted)
	}
	legacy, err := decryptCardDAVValue(cfg, "plaintext")
	if err != nil || legacy != "plaintext" {
		t.Errorf("expected legacy plaintext passthrough, got %q err %v", legacy, err)
	}
	other := CardDAVConfig{KeyFile: filepath.Join(dir, "other.key")}
	if _, err := decryptCardDAVValue(other, encrypted); err == nil {
		t.Error("expected a decryption error with a different key")
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Errorf("expected the key file to exist: %v", err)
	}
}

func TestCardDAVUnlockKeyFromSecret(t *testing.T) {
	first := cardDAVUnlockKeyFromSecret("correct horse battery staple")
	second := cardDAVUnlockKeyFromSecret("correct horse battery staple")
	other := cardDAVUnlockKeyFromSecret("a different secret")
	if len(first) != 32 {
		t.Fatalf("expected a 32-byte key, got %d", len(first))
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("expected the same secret to derive the same key")
	}
	if bytes.Equal(first, other) {
		t.Fatalf("expected different secrets to derive different keys")
	}
}

func TestCardDAVRAMKeyPrecedence(t *testing.T) {
	previous := cardDAVProcessKeySource
	setCardDAVProcessKeyResolver(func() ([]byte, error) {
		return nil, fmt.Errorf("resolver should not be consulted while unlocked")
	})
	defer setCardDAVProcessKeyResolver(previous)
	clearCardDAVRAMKey()
	if _, err := cardDAVProcessKey(); err == nil {
		t.Fatalf("expected an error without any key material")
	}
	ramKey := []byte("0123456789abcdef0123456789abcdef")
	setCardDAVRAMKey(ramKey)
	key, err := cardDAVProcessKey()
	if err != nil {
		t.Fatalf("expected the in-memory key to win: %v", err)
	}
	if string(key) != string(ramKey) {
		t.Fatalf("expected the in-memory key, got something else")
	}
	clearCardDAVRAMKey()
	if _, err := cardDAVProcessKey(); err == nil {
		t.Fatalf("expected an error after locking again")
	}
}
