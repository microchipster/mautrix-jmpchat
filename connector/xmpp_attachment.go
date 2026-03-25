package connector

import (
	"net/url"
	"path"
	"regexp"
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

func classifyContentTypeMessageType(contentType string) event.MessageType {
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return event.MsgImage
	case strings.HasPrefix(contentType, "video/"):
		return event.MsgVideo
	case strings.HasPrefix(contentType, "audio/"):
		return event.MsgAudio
	case contentType != "":
		return event.MsgFile
	default:
		return event.MsgText
	}
}
