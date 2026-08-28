package connector

import (
	"testing"

	"gosrc.io/xmpp/stanza"
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

func TestApplyXMPPMessageFormattingMarkdown(t *testing.T) {
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: "Was *very* _good_ ~yesterday~ `code`"}
	applyXMPPMessageFormatting(content, "")

	if content.Format != event.FormatHTML {
		t.Fatalf("unexpected format: %q", content.Format)
	}
	if content.FormattedBody != "Was <strong>very</strong> <em>good</em> <del>yesterday</del> <code>code</code>" {
		t.Fatalf("unexpected formatted body: %q", content.FormattedBody)
	}
	if content.Body != "Was *very* _good_ ~yesterday~ `code`" {
		t.Fatalf("unexpected raw body: %q", content.Body)
	}
}

func TestApplyXMPPMessageFormattingNestedMarkdown(t *testing.T) {
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: "***important*** and https://example.com"}
	applyXMPPMessageFormatting(content, "")

	want := `<strong><em>important</em></strong> and <a href="https://example.com">https://example.com</a>`
	if content.FormattedBody != want {
		t.Fatalf("unexpected nested formatted body: %q", content.FormattedBody)
	}
}

func TestFormatXMPPHTMLSanitizesAndPreservesFormatting(t *testing.T) {
	formatted, ok := formatXMPPHTML(`<strong>important</strong><br/><a href="https://example.com?q=1&amp;x=2">link</a><script>alert(1)</script><a href="javascript:alert(2)">unsafe</a>`)
	if !ok {
		t.Fatal("expected XHTML formatting")
	}
	if formatted != `<strong>important</strong><br/><a href="https://example.com?q=1&amp;x=2">link</a>unsafe` {
		t.Fatalf("unexpected sanitized XHTML: %q", formatted)
	}
}

func TestFormatXMPPHTMLAllowsOnlySafeLinkSchemes(t *testing.T) {
	formatted, ok := formatXMPPHTML(`<a href="mailto:alice@example.com">mail</a><a href="//example.com">protocol-relative</a><a href="data:text/html,evil">data</a>`)
	if !ok {
		t.Fatal("expected text from XHTML fragment")
	}
	want := `<a href="mailto:alice@example.com">mail</a>protocol-relativedata`
	if formatted != want {
		t.Fatalf("unexpected safe-link output: %q", formatted)
	}
}

func TestApplyXMPPMessageFormattingUsesXHTML(t *testing.T) {
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: "Was *very* good"}
	applyXMPPMessageFormatting(content, `<em>Was <strong>very</strong> good</em>`)

	if content.Format != event.FormatHTML {
		t.Fatalf("unexpected format: %q", content.Format)
	}
	if content.FormattedBody != `<em>Was <strong>very</strong> good</em>` {
		t.Fatalf("unexpected XHTML body: %q", content.FormattedBody)
	}
}

func TestApplyXMPPMessageFormattingUnmatchedMarkup(t *testing.T) {
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: "literal * marker"}
	applyXMPPMessageFormatting(content, "")

	if content.Format != "" || content.FormattedBody != "" {
		t.Fatalf("unmatched markup should not create HTML: format=%q body=%q", content.Format, content.FormattedBody)
	}
}

func TestExtractXMPPHTML(t *testing.T) {
	msg := stanza.Message{Extensions: []stanza.MsgExtension{
		&stanza.HTML{Body: stanza.HTMLBody{InnerXML: `<strong>styled</strong>`}},
	}}
	if got := extractXMPPHTML(&msg); got != `<strong>styled</strong>` {
		t.Fatalf("extractXMPPHTML() = %q, want XHTML payload", got)
	}
	if got := extractXMPPHTML(nil); got != "" {
		t.Fatalf("extractXMPPHTML(nil) = %q, want empty string", got)
	}
}
