// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"net"
	"reflect"
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
		{Name: "Jos\\, Jr", Phones: []string{"tel:+15550102100"}},
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
