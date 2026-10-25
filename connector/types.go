package connector

import (
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

// LoginMetadata stores additional remote-auth/session data in the bridge database.
// Use this for connector-specific fields that don't fit in core bridge tables.
type LoginMetadata struct {
	RemoteUserID        string                        `json:"remote_user_id,omitempty"` // XMPP JID
	AccessToken         string                        `json:"access_token,omitempty"`   // XMPP Password
	ExpiresAt           time.Time                     `json:"expires_at,omitempty"`
	DeviceID            string                        `json:"device_id,omitempty"`
	Scopes              []string                      `json:"scopes,omitempty"`
	LastSyncAt          *time.Time                    `json:"last_sync_at,omitempty"`
	PendingGatewayForms map[string]PendingGatewayForm `json:"pending_gateway_forms,omitempty"`
}

type PendingGatewayForm struct {
	TargetJID       string                `json:"target_jid,omitempty"`
	Node            string                `json:"node,omitempty"`
	RemoteSessionID string                `json:"remote_session_id,omitempty"`
	Title           string                `json:"title,omitempty"`
	Instructions    []string              `json:"instructions,omitempty"`
	Fields          []PendingGatewayField `json:"fields,omitempty"`
	Notes           []string              `json:"notes,omitempty"`
	AllowedActions  []string              `json:"allowed_actions,omitempty"`
	DefaultAction   string                `json:"default_action,omitempty"`
	CreatedAt       time.Time             `json:"created_at,omitempty"`
	LastStatus      string                `json:"last_status,omitempty"`
}

type PendingGatewayField struct {
	Var         string                 `json:"var,omitempty"`
	Type        string                 `json:"type,omitempty"`
	Label       string                 `json:"label,omitempty"`
	Description string                 `json:"description,omitempty"`
	Required    bool                   `json:"required,omitempty"`
	Values      []string               `json:"values,omitempty"`
	Options     []PendingGatewayOption `json:"options,omitempty"`
}

type PendingGatewayOption struct {
	Label string `json:"label,omitempty"`
	Value string `json:"value,omitempty"`
}

// New creates a new instance for database registration.
func (m *LoginMetadata) New() any {
	return &LoginMetadata{}
}

// GhostMetadata stores additional remote metadata for a Matrix ghost.
type GhostMetadata struct {
	RemoteUserID string `json:"remote_user_id,omitempty"`
	RemoteName   string `json:"remote_name,omitempty"`
	AvatarURL    string `json:"avatar_url,omitempty"`
	CustomName   string `json:"custom_name,omitempty"`
}

// New creates a new instance for database registration.
func (m *GhostMetadata) New() any {
	return &GhostMetadata{}
}

// PortalMetadata stores additional remote metadata for a Matrix portal (room).
type PortalMetadata struct {
	RemoteRoomID        string              `json:"remote_room_id,omitempty"`
	OtherUserID         networkid.UserID    `json:"other_user_id,omitempty"` // For DMs
	InitialName         string              `json:"initial_name,omitempty"`
	InitialAvatar       id.ContentURIString `json:"initial_avatar_mxc,omitempty"`
	CreatedAt           time.Time           `json:"created_at,omitempty"`
	LastSyncedAt        *time.Time          `json:"last_synced_at,omitempty"`
	RemoteTopic         string              `json:"remote_topic,omitempty"`
	LastMessageID       string              `json:"last_message_id,omitempty"`
	LastServerMessageID string              `json:"last_server_message_id,omitempty"`
	Tags                []string            `json:"tags,omitempty"`
	Notes               map[string]string   `json:"notes,omitempty"`
	IsMUC               bool                `json:"is_muc,omitempty"`
}

// New creates a new instance for database registration.
func (m *PortalMetadata) New() any {
	return &PortalMetadata{}
}

// MessageMetadata stores the plain-text body of a bridged message.
//
// BridgeV2 does not persist message bodies by default, but iMessage-style
// tapbacks (SMS texts such as `Liked “see you at 8”`) only carry the quoted
// text of the message they react to. Keeping the body around lets the bridge
// find that message and turn the tapback into a Matrix reaction instead of
// forwarding it as a confusing text message.
type MessageMetadata struct {
	Body          string `json:"body,omitempty"`
	HasAttachment bool   `json:"has_attachment,omitempty"`

	// XMPP identifiers seen on the original stanza. They let edits, retractions
	// and reactions resolve a stanza reference (<replace id>, <retract id>, ...)
	// back to the bridged message, even when the reference names a different
	// identifier than the one the bridge used as the message ID.
	XMPPMessageID string `json:"xmpp_message_id,omitempty"`
	OriginID      string `json:"origin_id,omitempty"`
	StanzaID      string `json:"stanza_id,omitempty"`
}

// New creates a new instance for database registration.
func (m *MessageMetadata) New() any {
	return &MessageMetadata{}
}
