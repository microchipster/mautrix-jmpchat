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
	"maunium.net/go/mautrix/id"
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
	if !c.bridge.Config.PersonalFilteringSpaces {
		c.bridge.Config.PersonalFilteringSpaces = true
		c.log.Info().Msg("Enabled personal_filtering_spaces by default")
	}
	if proc, ok := c.bridge.Commands.(*commands.Processor); ok {
		proc.AddHandler(CommandPing)
		proc.AddHandler(CommandSync)
		proc.AddHandler(CommandSyncStatus)
		proc.AddHandler(CommandStartChat)
		proc.AddHandler(CommandListContacts)
		proc.AddHandler(CommandListContacted)
		proc.AddHandler(CommandBlockContact)
		proc.AddHandler(CommandReportContact)
		proc.AddHandler(CommandListUsers)
		proc.AddHandler(CommandListLogins)
		proc.AddHandler(CommandSetName)
		proc.AddHandler(CommandJoinRoom)
		proc.AddHandler(CommandListDiscoveredRooms)
		proc.AddHandler(CommandGatewayActions)
		proc.AddHandler(CommandGatewayForm)
		proc.AddHandler(CommandGatewaySubmit)
		proc.AddHandler(CommandDeleteAllPortals)
		proc.AddHandler(CommandLogout)
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
	return &bridgev2.NetworkGeneralCapabilities{
		Provisioning: bridgev2.ProvisioningCapabilities{
			GroupCreation: map[string]bridgev2.GroupTypeCapabilities{
				"default": {
					TypeDescription: "Cheogram phone-number group chat",
					Name: bridgev2.GroupFieldCapability{
						Allowed: true,
					},
					Participants: bridgev2.GroupFieldCapability{
						Allowed:   true,
						Required:  true,
						MinLength: 2,
					},
				},
			},
		},
	}
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

	go c.ensureSpaceRoomAndPopulate(login)
	client.connectMu.Lock()
	if !client.connectStarted {
		client.connectStarted = true
		go client.Connect(context.Background())
	}
	client.connectMu.Unlock()

	return nil
}

func (c *MyConnector) ensureSpaceRoomAndPopulate(login *bridgev2.UserLogin) {
	if !c.bridge.Config.PersonalFilteringSpaces {
		return
	}
	ctx := context.Background()
	user := login.User
	log := c.log.With().Str("user_mxid", string(user.MXID)).Str("login_id", string(login.ID)).Logger()
	ctx = log.WithContext(ctx)

	hadMgmtRoom := user.ManagementRoom != ""
	spaceRoom, err := login.GetSpaceRoom(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to get space room")
		return
	}
	if spaceRoom == "" {
		log.Debug().Msg("Space room not available")
		return
	}
	log.Info().Stringer("space_room", spaceRoom).Msg("Ensured personal space room")

	c.ensureManagementRoomInSpace(ctx, login, spaceRoom, !hadMgmtRoom)
	c.addExistingPortalsToSpace(ctx, login)
}

func (c *MyConnector) ensureManagementRoomInSpace(ctx context.Context, login *bridgev2.UserLogin, spaceRoom id.RoomID, sendHelp bool) {
	user := login.User
	log := zerolog.Ctx(ctx)
	mgmtRoom := user.ManagementRoom
	if mgmtRoom == "" {
		log.Debug().Msg("Management room not available yet, skipping space link")
		return
	}
	if sendHelp {
		sendNotice(c.bridge.Bot, mgmtRoom, managementRoomHelpText())
	}

	_, err := c.bridge.Bot.SendState(ctx, spaceRoom, event.StateSpaceChild, mgmtRoom.String(), &event.Content{
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

func managementRoomHelpText() string {
	return "Quick help:\n" +
		"- login <jid>\n" +
		"- logout [login_id] (soft)\n" +
		"- logout [login_id] purge (delete rooms + space)\n" +
		"- delete-all-portals yes\n" +
		"- ping\n" +
		"- sync [full|30d] (default 30d)\n" +
		"- sync-status\n" +
		"- create <jid> [jid...]\n" +
		"- list-contacts\n" +
		"- list-contacted\n" +
		"- block [--report] <jid>\n" +
		"- report <jid>\n" +
		"- gateway-actions [jid]\n" +
		"- gateway-form <jid> <node>\n" +
		"- gateway-submit <session> field=value\n" +
		"- list-logins [--verbose]\n" +
		"- list-users\n" +
		"- set-name <jid> <name>"
}

func (c *MyConnector) addExistingPortalsToSpace(ctx context.Context, login *bridgev2.UserLogin) {
	log := zerolog.Ctx(ctx)
	userPortals, err := c.bridge.DB.UserPortal.GetAllForLogin(ctx, login.UserLogin)
	if err != nil {
		log.Err(err).Msg("Failed to list portals for space sync")
		return
	}
	for _, userPortal := range userPortals {
		portal, err := c.bridge.GetExistingPortalByKey(ctx, userPortal.Portal)
		if err != nil || portal == nil {
			continue
		}
		if err := login.AddPortalToSpace(ctx, portal, userPortal.CopyWithoutValues()); err != nil {
			log.Err(err).Msg("Failed to add portal to space")
		}
	}
}
