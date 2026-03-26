package connector

import (
	"html"
	"regexp"
	"strings"

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
