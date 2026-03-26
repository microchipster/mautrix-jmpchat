package connector

import (
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestFormatMatrixHTMLLinksPlainURL(t *testing.T) {
	formatted, ok := formatMatrixHTMLLinks("https://duck.ai")
	if !ok {
		t.Fatal("expected URL formatting")
	}
	if formatted != `<a href="https://duck.ai">https://duck.ai</a>` {
		t.Fatalf("unexpected formatted body: %q", formatted)
	}
}

func TestFormatMatrixHTMLLinksMixedText(t *testing.T) {
	formatted, ok := formatMatrixHTMLLinks("see https://duck.ai, thanks")
	if !ok {
		t.Fatal("expected URL formatting")
	}
	if formatted != `see <a href="https://duck.ai">https://duck.ai</a>, thanks` {
		t.Fatalf("unexpected formatted body: %q", formatted)
	}
}

func TestApplyMatrixLinkFormatting(t *testing.T) {
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: "https://duck.ai"}
	applyMatrixLinkFormatting(content)
	if content.Format != event.FormatHTML {
		t.Fatalf("unexpected format: %q", content.Format)
	}
	if content.FormattedBody != `<a href="https://duck.ai">https://duck.ai</a>` {
		t.Fatalf("unexpected formatted body: %q", content.FormattedBody)
	}
	if content.Body != "https://duck.ai" {
		t.Fatalf("unexpected raw body change: %q", content.Body)
	}
}
