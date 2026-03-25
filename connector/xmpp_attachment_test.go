package connector

import (
	"testing"

	"gosrc.io/xmpp/stanza"
)

func TestExtractXMPPAttachmentFromReference(t *testing.T) {
	msg := stanza.Message{
		Subject: "Voicemail Recording",
		Extensions: []stanza.MsgExtension{
			&XMPPReference{
				URI: "https://example.com/fallback.mp3",
				MediaSharing: &XMPPMediaSharing{
					FileV5: &XMPPSIMSFile{
						Name:      "thing.mp3",
						MediaType: "audio/mpeg",
						Size:      5000,
						Duration:  "PT10S",
					},
					Sources: &XMPPSIMSSources{
						Reference: &XMPPSIMSReference{URI: "https://example.com/thing.mp3"},
					},
				},
			},
		},
	}

	attachment := extractXMPPAttachment(msg)
	if attachment == nil {
		t.Fatal("expected attachment")
	}
	if attachment.URL != "https://example.com/thing.mp3" {
		t.Fatalf("unexpected attachment url: %q", attachment.URL)
	}
	if attachment.FileName != "thing.mp3" {
		t.Fatalf("unexpected attachment name: %q", attachment.FileName)
	}
	if attachment.ContentType != "audio/mpeg" {
		t.Fatalf("unexpected attachment content type: %q", attachment.ContentType)
	}
	if attachment.Size != 5000 {
		t.Fatalf("unexpected attachment size: %d", attachment.Size)
	}
	if attachment.Description != "Voicemail Recording" {
		t.Fatalf("unexpected attachment description: %q", attachment.Description)
	}
	if attachment.DurationMS != 10000 {
		t.Fatalf("unexpected attachment duration: %d", attachment.DurationMS)
	}
}

func TestExtractXMPPAttachmentFromOOB(t *testing.T) {
	msg := stanza.Message{
		Extensions: []stanza.MsgExtension{
			&stanza.OOB{URL: "https://example.com/audio.ogg", Desc: "Voice mail"},
		},
	}

	attachment := extractXMPPAttachment(msg)
	if attachment == nil {
		t.Fatal("expected attachment")
	}
	if attachment.URL != "https://example.com/audio.ogg" {
		t.Fatalf("unexpected attachment url: %q", attachment.URL)
	}
	if attachment.FileName != "audio.ogg" {
		t.Fatalf("unexpected derived attachment name: %q", attachment.FileName)
	}
	if attachment.Description != "Voice mail" {
		t.Fatalf("unexpected attachment description: %q", attachment.Description)
	}
}

func TestParseSIMSDurationMillis(t *testing.T) {
	tests := map[string]int{
		"":           0,
		"PT10S":      10000,
		"PT1M2.5S":   62500,
		"P1DT2H3M4S": 93784000,
	}

	for input, expected := range tests {
		if got := parseSIMSDurationMillis(input); got != expected {
			t.Fatalf("parseSIMSDurationMillis(%q) = %d, want %d", input, got, expected)
		}
	}
}
