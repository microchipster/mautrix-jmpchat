package connector

import (
	"html"
	"net/url"
	"regexp"
	"strings"

	htmlnode "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/event"
)

var urlInTextRegex = regexp.MustCompile(`https?://[^\s<>"]+`)

func applyMatrixLinkFormatting(content *event.MessageEventContent) {
	if content == nil || strings.TrimSpace(content.Body) == "" {
		return
	}
	formatted, ok := formatMatrixHTMLLinks(content.Body)
	if !ok {
		return
	}
	content.Format = event.FormatHTML
	content.FormattedBody = formatted
}

// applyXMPPMessageFormatting converts Cheogram's XHTML or inline markup to
// Matrix HTML while keeping Body as the plain-text fallback.
func applyXMPPMessageFormatting(content *event.MessageEventContent, xhtml string) {
	if content == nil || strings.TrimSpace(content.Body) == "" {
		return
	}
	if strings.TrimSpace(xhtml) != "" {
		if formatted, ok := formatXMPPHTML(xhtml); ok {
			content.Format = event.FormatHTML
			content.FormattedBody = formatted
			return
		}
	}
	if formatted, ok := formatCheogramInlineMarkup(content.Body); ok {
		content.Format = event.FormatHTML
		content.FormattedBody = formatted
		return
	}
	applyMatrixLinkFormatting(content)
}

func formatXMPPHTML(innerXML string) (string, bool) {
	context := &htmlnode.Node{Type: htmlnode.ElementNode, DataAtom: atom.Body, Data: "body"}
	nodes, err := htmlnode.ParseFragment(strings.NewReader(innerXML), context)
	if err != nil || len(nodes) == 0 {
		return "", false
	}
	var builder strings.Builder
	for _, node := range nodes {
		writeSafeXMPPHTML(&builder, node)
	}
	return builder.String(), builder.Len() > 0
}

func writeSafeXMPPHTML(builder *strings.Builder, node *htmlnode.Node) {
	switch node.Type {
	case htmlnode.TextNode:
		builder.WriteString(html.EscapeString(node.Data))
	case htmlnode.ElementNode:
		tag := strings.ToLower(node.Data)
		switch tag {
		case "html", "body", "span":
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				writeSafeXMPPHTML(builder, child)
			}
		case "br":
			builder.WriteString("<br/>")
		case "p", "div", "blockquote", "pre", "code", "strong", "em", "b", "i", "u", "s", "strike":
			builder.WriteByte('<')
			builder.WriteString(tag)
			builder.WriteByte('>')
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				writeSafeXMPPHTML(builder, child)
			}
			builder.WriteString("</")
			builder.WriteString(tag)
			builder.WriteByte('>')
		case "a":
			if href, ok := safeXMPPLink(node); ok {
				builder.WriteString(`<a href="`)
				builder.WriteString(html.EscapeString(href))
				builder.WriteString(`">`)
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					writeSafeXMPPHTML(builder, child)
				}
				builder.WriteString("</a>")
				return
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				writeSafeXMPPHTML(builder, child)
			}
		case "script", "style", "iframe", "object", "embed":
			return
		default:
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				writeSafeXMPPHTML(builder, child)
			}
		}
	case htmlnode.DocumentNode, htmlnode.DoctypeNode:
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			writeSafeXMPPHTML(builder, child)
		}
	}
}

func safeXMPPLink(node *htmlnode.Node) (string, bool) {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, "href") {
			parsed, err := url.Parse(strings.TrimSpace(attr.Val))
			if err != nil {
				return "", false
			}
			switch strings.ToLower(parsed.Scheme) {
			case "http", "https", "mailto":
				return parsed.String(), true
			default:
				return "", false
			}
		}
	}
	return "", false
}

func formatCheogramInlineMarkup(body string) (string, bool) {
	formatted, changed := formatCheogramInlineSegment(body)
	return formatted, changed
}

func formatCheogramInlineSegment(text string) (string, bool) {
	var builder strings.Builder
	changed := false
	plainStart := 0
	for i := 0; i < len(text); {
		marker, openTag, closeTag, width := cheogramMarkupMarker(text[i:])
		if marker == "" {
			i++
			continue
		}
		closeOffset := strings.Index(text[i+width:], marker)
		if closeOffset < 1 {
			i += width
			continue
		}
		closeStart := i + width + closeOffset
		inner := text[i+width : closeStart]
		if strings.TrimSpace(inner) == "" || strings.TrimSpace(inner) != inner {
			i += width
			continue
		}
		if plainStart < i {
			plain, hasLinks := formatMatrixHTMLLinks(text[plainStart:i])
			if !hasLinks {
				plain = html.EscapeString(text[plainStart:i])
			}
			builder.WriteString(plain)
			changed = changed || hasLinks
		}
		innerHTML := html.EscapeString(inner)
		if marker != "`" {
			var innerChanged bool
			innerHTML, innerChanged = formatCheogramInlineSegment(inner)
			changed = changed || innerChanged
		}
		builder.WriteString(openTag)
		builder.WriteString(innerHTML)
		builder.WriteString(closeTag)
		changed = true
		i = closeStart + width
		plainStart = i
	}
	if plainStart < len(text) {
		plain, hasLinks := formatMatrixHTMLLinks(text[plainStart:])
		if !hasLinks {
			plain = html.EscapeString(text[plainStart:])
		}
		builder.WriteString(plain)
		changed = changed || hasLinks
	}
	return builder.String(), changed
}

func cheogramMarkupMarker(text string) (string, string, string, int) {
	if strings.HasPrefix(text, "***") {
		return "***", "<strong><em>", "</em></strong>", 3
	}
	if strings.HasPrefix(text, "___") {
		return "___", "<strong><em>", "</em></strong>", 3
	}
	switch text[0] {
	case '*':
		return "*", "<strong>", "</strong>", 1
	case '_':
		return "_", "<em>", "</em>", 1
	case '~':
		return "~", "<del>", "</del>", 1
	case '`':
		return "`", "<code>", "</code>", 1
	default:
		return "", "", "", 0
	}
}

func extractXMPPHTML(msg *stanza.Message) string {
	if msg == nil {
		return ""
	}
	var xhtml stanza.HTML
	if !msg.Get(&xhtml) {
		return ""
	}
	return xhtml.Body.InnerXML
}

func formatMatrixHTMLLinks(body string) (string, bool) {
	matches := urlInTextRegex.FindAllStringIndex(body, -1)
	if len(matches) == 0 {
		return "", false
	}
	var builder strings.Builder
	last := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		urlText := body[start:end]
		trimmedURL, trailing := trimURLTrailingPunctuation(urlText)
		if trimmedURL == "" {
			continue
		}
		builder.WriteString(html.EscapeString(body[last:start]))
		escapedURL := html.EscapeString(trimmedURL)
		builder.WriteString(`<a href="`)
		builder.WriteString(escapedURL)
		builder.WriteString(`">`)
		builder.WriteString(escapedURL)
		builder.WriteString(`</a>`)
		builder.WriteString(html.EscapeString(trailing))
		last = end
	}
	builder.WriteString(html.EscapeString(body[last:]))
	return builder.String(), true
}

func trimURLTrailingPunctuation(urlText string) (string, string) {
	trimmed := urlText
	trailing := ""
	for len(trimmed) > 0 {
		last := trimmed[len(trimmed)-1]
		if !strings.ContainsRune(`.,!?;:)`, rune(last)) {
			break
		}
		trimmed = trimmed[:len(trimmed)-1]
		trailing = string(last) + trailing
	}
	return trimmed, trailing
}
