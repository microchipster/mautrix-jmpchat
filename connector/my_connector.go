package connector

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/configupgrade"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
)

// Ensure MyConnector implements NetworkConnector.
var _ bridgev2.NetworkConnector = (*MyConnector)(nil)

// MyConnector implements the NetworkConnector interface.
type MyConnector struct {
	log    zerolog.Logger
	bridge *bridgev2.Bridge
}

// NewMyConnector creates a new instance of MyConnector.
func NewMyConnector(log zerolog.Logger) *MyConnector {
	return &MyConnector{
		log: log.With().Str("component", "network-connector").Logger(),
	}
}

// Init initializes the connector with the bridge instance.
func (c *MyConnector) Init(br *bridgev2.Bridge) {
	c.bridge = br
	c.log = c.bridge.Log
	c.log.Info().Msg("MyConnector Init called")
	if proc, ok := c.bridge.Commands.(*commands.Processor); ok {
		proc.AddHandler(CommandSync)
		proc.AddHandler(CommandSyncStatus)
		proc.AddHandler(CommandCreate)
		proc.AddHandler(CommandListUsers)
		proc.AddHandler(CommandSetName)
		proc.AddHandler(CommandNuke)
		proc.AddHandler(CommandLogoutSoft)
		c.log.Info().Msg("Registered sync command")
	} else {
		c.log.Warn().Msg("Command processor not available for custom commands")
	}
}

// GetName implements bridgev2.NetworkConnector.
func (c *MyConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:          "JMPChat",
		NetworkURL:           "https://jmp.chat",
		NetworkIcon:          "",
		NetworkID:            "jmpchat",
		BeeperBridgeType:     "jmpchat",
		DefaultPort:          29319,
		DefaultCommandPrefix: "!jmpchat",
	}
}

// GetNetworkID implements bridgev2.NetworkConnector.
func (c *MyConnector) GetNetworkID() string {
	return c.GetName().NetworkID
}

// GetCapabilities implements bridgev2.NetworkConnector.
func (c *MyConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{}
}

// GetDBMetaTypes implements bridgev2.NetworkConnector.
func (c *MyConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Portal: func() any {
			return &PortalMetadata{}
		},
		Ghost: func() any {
			return &GhostMetadata{}
		},
		UserLogin: func() any {
			return &LoginMetadata{}
		},
	}
}

// GetLoginFlows implements bridgev2.NetworkConnector.
func (c *MyConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		ID:          LoginFlowIDUsernamePassword,
		Name:        "Username & Password",
		Description: "Log in using a username and password (no actual validation).",
	}}
}

// CreateLogin implements bridgev2.NetworkConnector.
func (c *MyConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != LoginFlowIDUsernamePassword {
		return nil, fmt.Errorf("unsupported login flow ID: %s", flowID)
	}
	return &SimpleLogin{
		User: user,
		Main: c,
		Log:  user.Log.With().Str("action", "login").Str("flow", flowID).Logger(),
	}, nil
}

// GetConfig implements bridgev2.NetworkConnector.
func (c *MyConnector) GetConfig() (string, any, configupgrade.Upgrader) {
	return "simple-config.yaml", nil, nil
}

// GetBridgeInfoVersion implements bridgev2.NetworkConnector.
func (c *MyConnector) GetBridgeInfoVersion() (int, int) {
	return 1, 0
}

// Start implements bridgev2.NetworkConnector.
func (c *MyConnector) Start(ctx context.Context) error {
	c.log.Info().Msg("MyConnector Start called")
	return nil
}

// Stop implements bridgev2.NetworkConnector.
func (c *MyConnector) Stop(ctx context.Context) error {
	c.log.Info().Msg("MyConnector Stop called")
	return nil
}

// LoadUserLogin implements bridgev2.NetworkConnector.
func (c *MyConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	c.log.Info().
		Str("user_id", string(login.ID)).
		Str("remote_name", login.RemoteName).
		Str("mxid", string(login.User.MXID)).
		Msg("LoadUserLogin called")

	client := &MyNetworkClient{
		log:       c.log.With().Str("user_id", string(login.ID)).Logger(),
		bridge:    c.bridge,
		login:     login,
		connector: c,
	}

	login.Client = client

	c.log.Info().
		Str("user_id", string(login.ID)).
		Str("remote_name", login.RemoteName).
		Interface("client_type", client).
		Msg("Created and stored MyNetworkClient")

	go c.ensureManagementRoomInSpace(login)

	return nil
}

func (c *MyConnector) ensureManagementRoomInSpace(login *bridgev2.UserLogin) {
	if !c.bridge.Config.PersonalFilteringSpaces {
		return
	}
	ctx := context.Background()
	user := login.User
	log := c.log.With().Str("user_mxid", string(user.MXID)).Str("login_id", string(login.ID)).Logger()
	ctx = log.WithContext(ctx)

	spaceRoom, err := login.GetSpaceRoom(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to get space room")
		return
	}
	if spaceRoom == "" {
		return
	}
	mgmtRoom, err := user.GetManagementRoom(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to get management room")
		return
	}
	if mgmtRoom == "" {
		return
	}
	_, err = c.bridge.Bot.SendState(ctx, spaceRoom, event.StateSpaceChild, mgmtRoom.String(), &event.Content{
		Parsed: &event.SpaceChildEventContent{
			Via: []string{c.bridge.Matrix.ServerName()},
		},
	}, time.Now())
	if err != nil {
		log.Err(err).Stringer("space_room", spaceRoom).Stringer("mgmt_room", mgmtRoom).Msg("Failed to add management room to space")
		return
	}
	log.Debug().Stringer("space_room", spaceRoom).Stringer("mgmt_room", mgmtRoom).Msg("Added management room to space")
}
