package connector

import (
	"testing"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/event"
)

func TestExtractXMPPAttachmentFromReference(t *testing.T) {
	msg := stanza.Message{
		Subject: stanza.StringPtr("Voicemail Recording"),
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

func TestExtractXMPPAttachmentCaptionPrefersBodyWithoutFallbackURL(t *testing.T) {
	msg := stanza.Message{
		Body:    stanza.StringPtr("Family picture\nhttps://example.com/pic.jpg"),
		Subject: stanza.StringPtr("Holiday"),
		Extensions: []stanza.MsgExtension{
			&stanza.OOB{URL: "https://example.com/pic.jpg", Desc: "Photo"},
			&XMPPFallback{
				For:  "jabber:x:oob",
				Body: []XMPPFallbackBody{{Start: 15, End: 42}},
			},
		},
	}

	attachment := extractXMPPAttachment(msg)
	caption := extractXMPPAttachmentCaption(msg, attachment)
	if caption != "Holiday\n\nFamily picture" {
		t.Fatalf("unexpected caption: %q", caption)
	}
}

func TestExtractXMPPAttachmentCaptionFallsBackToSubject(t *testing.T) {
	msg := stanza.Message{
		Body:    stanza.StringPtr("https://example.com/vm.ogg"),
		Subject: stanza.StringPtr("Voicemail Recording"),
		Extensions: []stanza.MsgExtension{
			&stanza.OOB{URL: "https://example.com/vm.ogg"},
		},
	}

	attachment := extractXMPPAttachment(msg)
	caption := extractXMPPAttachmentCaption(msg, attachment)
	if caption != "Voicemail Recording" {
		t.Fatalf("unexpected caption: %q", caption)
	}
}

func TestExtractXMPPAttachmentCaptionMergesSubjectAndCaption(t *testing.T) {
	msg := stanza.Message{
		Body:    stanza.StringPtr("Here is the photo\nhttps://example.com/pic.jpg"),
		Subject: stanza.StringPtr("Holiday"),
		Extensions: []stanza.MsgExtension{
			&stanza.OOB{URL: "https://example.com/pic.jpg"},
			&XMPPFallback{For: "jabber:x:oob", Body: []XMPPFallbackBody{{Start: 18, End: 45}}},
		},
	}

	attachment := extractXMPPAttachment(msg)
	caption := extractXMPPAttachmentCaption(msg, attachment)
	if caption != "Holiday\n\nHere is the photo" {
		t.Fatalf("unexpected merged caption: %q", caption)
	}
}

func TestExtractXMPPAttachmentCaptionStripsReplyAndOOBFallbacks(t *testing.T) {
	msg := stanza.Message{
		Body: stanza.StringPtr(`> Alice wrote:
old message

Here is the photo
https://example.com/pic.jpg`),
		Extensions: []stanza.MsgExtension{
			&stanza.OOB{URL: "https://example.com/pic.jpg"},
			&XMPPFallback{For: NSReply, Body: []XMPPFallbackBody{{Start: 0, End: 27}}},
			&XMPPFallback{For: "jabber:x:oob", Body: []XMPPFallbackBody{{Start: 46, End: 73}}},
		},
	}

	attachment := extractXMPPAttachment(msg)
	caption := extractXMPPAttachmentCaption(msg, attachment)
	if caption != "Here is the photo" {
		t.Fatalf("unexpected caption after stripping fallbacks: %q", caption)
	}
}

func TestBuildOutgoingXMPPAttachmentReference(t *testing.T) {
	content := &event.MessageEventContent{
		MsgType:  event.MsgAudio,
		Body:     "Voicemail Recording",
		FileName: "voicemail.ogg",
		Info: &event.FileInfo{
			MimeType: "audio/ogg",
			Size:     4096,
			Duration: 12500,
		},
	}

	ref := buildOutgoingXMPPAttachmentReference(content, "https://example.com/voicemail.ogg")
	if ref == nil {
		t.Fatal("expected attachment reference")
	}
	if ref.Type != "data" || ref.URI != "https://example.com/voicemail.ogg" {
		t.Fatalf("unexpected reference metadata: %#v", ref)
	}
	if ref.MediaSharing == nil || ref.MediaSharing.File() == nil {
		t.Fatal("expected media-sharing file metadata")
	}
	file := ref.MediaSharing.File()
	if file.Name != "voicemail.ogg" || file.MediaType != "audio/ogg" || file.Size != 4096 {
		t.Fatalf("unexpected file metadata: %#v", file)
	}
	if file.Duration != "PT12.500S" {
		t.Fatalf("unexpected duration: %q", file.Duration)
	}
}

func TestMergeXMPPSubjectAndBody(t *testing.T) {
	if got := mergeXMPPSubjectAndBody("Subject", "Body"); got != "Subject\n\nBody" {
		t.Fatalf("unexpected merged subject/body: %q", got)
	}
	if got := mergeXMPPSubjectAndBody("Subject", "Subject"); got != "Subject" {
		t.Fatalf("unexpected duplicate subject/body merge: %q", got)
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

func TestClassifyContentTypeMessageType(t *testing.T) {
	if got := classifyContentTypeMessageType("text/html", false); got != event.MsgText {
		t.Fatalf("expected plain URL content-type to stay text, got %q", got)
	}
	if got := classifyContentTypeMessageType("text/html", true); got != event.MsgFile {
		t.Fatalf("expected attachment content-type to map to file, got %q", got)
	}
	if got := classifyContentTypeMessageType("image/jpeg", false); got != event.MsgImage {
		t.Fatalf("expected image/jpeg to map to image, got %q", got)
	}
}

func TestIsLikelyXMPPVoiceNote(t *testing.T) {
	msg := stanza.Message{Subject: stanza.StringPtr("New Voicemail")}
	attachment := &xmppAttachment{ContentType: "audio/mpeg", FileName: "vm.mp3"}
	if !isLikelyXMPPVoiceNote(msg, attachment) {
		t.Fatal("expected voicemail audio to be treated as voice note")
	}
	attachment.Description = "podcast episode"
	msg.Subject = nil
	if isLikelyXMPPVoiceNote(msg, attachment) {
		t.Fatal("expected generic audio attachment to not be treated as voice note")
	}
}

func TestPreferredXMPPVoiceNoteBody(t *testing.T) {
	msg := stanza.Message{Subject: stanza.StringPtr("New Voicemail")}
	attachment := &xmppAttachment{ContentType: "audio/mpeg", FileName: "vm.mp3", URL: "https://example.com/vm.mp3"}
	if got := preferredXMPPVoiceNoteBody(msg, attachment, attachment.URL); got != "New Voicemail" {
		t.Fatalf("unexpected preferred voice note body: %q", got)
	}
	msg.Subject = nil
	attachment.Description = "Voicemail Recording"
	if got := preferredXMPPVoiceNoteBody(msg, attachment, attachment.FileName); got != "Voicemail Recording" {
		t.Fatalf("unexpected description-based voice note body: %q", got)
	}
	attachment.Description = ""
	msg.Body = stanza.StringPtr("voice note")
	if got := preferredXMPPVoiceNoteBody(msg, attachment, attachment.FileName); got != "Voicemail" {
		t.Fatalf("unexpected fallback voice note body: %q", got)
	}
}
