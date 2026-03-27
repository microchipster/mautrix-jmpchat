package connector

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gosrc.io/xmpp/stanza"
	"maunium.net/go/mautrix/event"
)

type xmppAttachment struct {
	URL         string
	Description string
	FileName    string
	ContentType string
	Size        int
	Width       int
	Height      int
	DurationMS  int
}

func extractXMPPAttachment(msg stanza.Message) *xmppAttachment {
	attachment := &xmppAttachment{}

	var ref XMPPReference
	if msg.Get(&ref) {
		attachment.URL = strings.TrimSpace(ref.URI)
		if media := ref.MediaSharing; media != nil {
			if media.Sources != nil && media.Sources.Reference != nil {
				if sourceURI := strings.TrimSpace(media.Sources.Reference.URI); sourceURI != "" {
					attachment.URL = sourceURI
				}
			}
			if file := media.File(); file != nil {
				attachment.FileName = strings.TrimSpace(file.Name)
				attachment.ContentType = strings.TrimSpace(file.MediaType)
				if file.Size > 0 {
					attachment.Size = int(file.Size)
				}
				attachment.Width = file.Width
				attachment.Height = file.Height
				attachment.DurationMS = parseSIMSDurationMillis(file.Duration)
			}
		}
	}

	var oob stanza.OOB
	if msg.Get(&oob) {
		if attachment.URL == "" {
			attachment.URL = strings.TrimSpace(oob.URL)
		}
		if attachment.Description == "" {
			attachment.Description = strings.TrimSpace(oob.Desc)
		}
	}

	if attachment.URL == "" {
		return nil
	}
	if attachment.FileName == "" {
		if parsed, err := url.Parse(attachment.URL); err == nil {
			fileName := path.Base(parsed.Path)
			if fileName != "" && fileName != "/" && fileName != "." {
				attachment.FileName = fileName
			}
		}
	}
	if attachment.Description == "" {
		attachment.Description = strings.TrimSpace(msg.Subject)
	}

	return attachment
}

var simsDurationPattern = regexp.MustCompile(`^P(?:([0-9]+)D)?(?:T(?:([0-9]+)H)?(?:([0-9]+)M)?(?:([0-9]+(?:\.[0-9]+)?)S)?)?$`)

func parseSIMSDurationMillis(input string) int {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0
	}
	matches := simsDurationPattern.FindStringSubmatch(input)
	if matches == nil {
		return 0
	}
	days, _ := strconv.Atoi(matches[1])
	hours, _ := strconv.Atoi(matches[2])
	minutes, _ := strconv.Atoi(matches[3])
	seconds, _ := strconv.ParseFloat(defaultString(matches[4], "0"), 64)
	totalMillis := (((float64(days)*24+float64(hours))*60+float64(minutes))*60 + seconds) * 1000
	return int(totalMillis)
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func extractXMPPAttachmentCaption(msg stanza.Message, attachment *xmppAttachment) string {
	caption := strings.TrimSpace(stripXMPPFallbackFromMessage(msg, NSReply, "jabber:x:oob"))
	if attachment != nil && attachment.URL != "" {
		caption = strings.TrimSpace(strings.ReplaceAll(caption, attachment.URL, ""))
	}
	caption = trimBlankLines(caption)
	if caption == "" && attachment != nil && strings.TrimSpace(attachment.Description) != "" {
		caption = trimBlankLines(strings.TrimSpace(attachment.Description))
	}
	return mergeXMPPSubjectAndBody(msg.Subject, caption)
}

func mergeXMPPSubjectAndBody(subject, body string) string {
	subject = trimBlankLines(strings.TrimSpace(subject))
	body = trimBlankLines(strings.TrimSpace(body))
	switch {
	case subject == "":
		return body
	case body == "":
		return subject
	case body == subject:
		return body
	default:
		return subject + "\n\n" + body
	}
}

func stripXMPPFallbackFromMessage(msg stanza.Message, fallbackFor ...string) string {
	body := msg.Body
	if body == "" {
		return ""
	}
	allowedFallbacks := make(map[string]struct{}, len(fallbackFor))
	for _, item := range fallbackFor {
		item = strings.TrimSpace(item)
		if item != "" {
			allowedFallbacks[item] = struct{}{}
		}
	}
	type bodyRange struct{ start, end int }
	ranges := make([]bodyRange, 0, len(msg.Extensions))
	for _, ext := range msg.Extensions {
		var fallback XMPPFallback
		switch typed := ext.(type) {
		case *XMPPFallback:
			fallback = *typed
		case XMPPFallback:
			fallback = typed
		default:
			continue
		}
		if len(allowedFallbacks) > 0 && fallback.For != "" {
			if _, ok := allowedFallbacks[fallback.For]; !ok {
				continue
			}
		}
		for _, item := range fallback.Body {
			if item.End <= item.Start || item.Start < 0 {
				continue
			}
			ranges = append(ranges, bodyRange{start: item.Start, end: item.End})
		}
	}
	if len(ranges) == 0 {
		return body
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start == ranges[j].start {
			return ranges[i].end < ranges[j].end
		}
		return ranges[i].start < ranges[j].start
	})
	runes := []rune(body)
	var builder strings.Builder
	last := 0
	for _, r := range ranges {
		start := clampRuneIndex(r.start, len(runes))
		end := clampRuneIndex(r.end, len(runes))
		if start < last {
			start = last
		}
		if end < start {
			end = start
		}
		if start > last {
			builder.WriteString(string(runes[last:start]))
		}
		last = end
	}
	if last < len(runes) {
		builder.WriteString(string(runes[last:]))
	}
	return builder.String()
}

func clampRuneIndex(index, max int) int {
	if index < 0 {
		return 0
	}
	if index > max {
		return max
	}
	return index
}

func trimBlankLines(input string) string {
	input = strings.TrimSpace(input)
	for strings.Contains(input, "\n\n\n") {
		input = strings.ReplaceAll(input, "\n\n\n", "\n\n")
	}
	return input
}

func classifyContentTypeMessageType(contentType string, treatGenericAsFile bool) event.MessageType {
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return event.MsgImage
	case strings.HasPrefix(contentType, "video/"):
		return event.MsgVideo
	case strings.HasPrefix(contentType, "audio/"):
		return event.MsgAudio
	case contentType != "" && treatGenericAsFile:
		return event.MsgFile
	default:
		return event.MsgText
	}
}

func isLikelyXMPPVoiceNote(msg stanza.Message, attachment *xmppAttachment) bool {
	if attachment == nil {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(attachment.ContentType))
	if !strings.HasPrefix(contentType, "audio/") {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		msg.Subject,
		msg.Body,
		attachment.Description,
		attachment.FileName,
	}, "\n")))
	return strings.Contains(text, "voicemail") || strings.Contains(text, "voice mail") || strings.Contains(text, "voice note")
}

func preferredXMPPVoiceNoteBody(msg stanza.Message, attachment *xmppAttachment, currentBody string) string {
	if !isLikelyXMPPVoiceNote(msg, attachment) {
		return currentBody
	}
	for _, candidate := range []string{
		strings.TrimSpace(msg.Subject),
		strings.TrimSpace(attachment.Description),
		trimBlankLines(strings.TrimSpace(currentBody)),
	} {
		if candidate == "" {
			continue
		}
		if looksLikeAttachmentPlaceholder(candidate, attachment) {
			continue
		}
		return candidate
	}
	return "Voicemail"
}

func looksLikeAttachmentPlaceholder(text string, attachment *xmppAttachment) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	if attachment != nil {
		if text == strings.TrimSpace(attachment.URL) || text == strings.TrimSpace(attachment.FileName) {
			return true
		}
	}
	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
		return true
	}
	return false
}

func buildOutgoingXMPPAttachmentReference(content *event.MessageEventContent, uploadedURL string) *XMPPReference {
	uploadedURL = strings.TrimSpace(uploadedURL)
	if content == nil || uploadedURL == "" {
		return nil
	}
	file := &XMPPSIMSFile{
		Name: content.GetFileName(),
	}
	if info := content.Info; info != nil {
		file.MediaType = strings.TrimSpace(info.MimeType)
		file.Size = int64(info.Size)
		file.Width = info.Width
		file.Height = info.Height
		file.Duration = formatSIMSDurationMillis(info.Duration)
	}
	return &XMPPReference{
		Type: "data",
		URI:  uploadedURL,
		MediaSharing: &XMPPMediaSharing{
			FileV5: file,
			Sources: &XMPPSIMSSources{
				Reference: &XMPPSIMSReference{URI: uploadedURL},
			},
		},
	}
}

func formatSIMSDurationMillis(durationMS int) string {
	if durationMS <= 0 {
		return ""
	}
	seconds := float64(durationMS) / 1000
	if durationMS%1000 == 0 {
		return fmt.Sprintf("PT%dS", durationMS/1000)
	}
	return fmt.Sprintf("PT%.3fS", seconds)
}
