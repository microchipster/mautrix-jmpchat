package connector

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"gosrc.io/xmpp"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
)

const (
	LoginFlowIDUsernamePassword = "xmpp-pass"
	LoginStepIDUsernamePassword = "xmpp-pass-input"
	LoginStepIDComplete         = "complete"
)

// SimpleLogin represents an ongoing username/password login attempt.
type SimpleLogin struct {
	User *bridgev2.User
	Main *MyConnector
	Log  zerolog.Logger
}

// Ensure SimpleLogin implements the required interface.
var _ bridgev2.LoginProcessUserInput = (*SimpleLogin)(nil)

// Start implements bridgev2.LoginProcessUserInput.
func (sl *SimpleLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	sl.Log.Debug().Msg("Starting username/password login flow")
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       LoginStepIDUsernamePassword,
		Instructions: "Enter your XMPP JID (e.g., user@cheogram.com) and password.",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type: bridgev2.LoginInputFieldTypeUsername,
					ID:   "username",
					Name: "JID",
				},
				{
					Type: bridgev2.LoginInputFieldTypePassword,
					ID:   "password",
					Name: "Password",
				},
			},
		},
	}, nil
}

// SubmitUserInput implements bridgev2.LoginProcessUserInput.
func (sl *SimpleLogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	username := input["username"]
	password := input["password"]

	if username == "" {
		return nil, fmt.Errorf("username cannot be empty")
	}

	sl.Log.Info().Str("username", username).Msg("Received login credentials, testing XMPP connection")

	// Verify XMPP credentials by making a test connection
	config := xmpp.Config{
		TransportConfiguration: xmpp.TransportConfiguration{},
		Jid:                    username,
		Credential:             xmpp.Password(password),
		Insecure:               false,
		StreamManagementEnable: true,
	}

	router := xmpp.NewRouter()
	client, err := xmpp.NewClient(&config, router, func(err error) {
		sl.Log.Err(err).Msg("XMPP Error during login test")
	})
	if err != nil {
		sl.Log.Err(err).Msg("Failed to connect to XMPP during login test")
		return nil, fmt.Errorf("failed to connect to XMPP: %w", err)
	}

	err = client.Connect()
	if err != nil {
		sl.Log.Err(err).Msg("Failed to establish XMPP connection")
		return nil, fmt.Errorf("invalid credentials or connection error: %w", err)
	}
	client.Disconnect()

	namespace := uuid.MustParse("f7a4f3e3-5d5a-4a9e-8d8a-3b0b9e8a1b2c")
	loginIDStr := uuid.NewSHA1(namespace, []byte(strings.ToLower(username))).String()
	var loginID networkid.UserLoginID = networkid.UserLoginID(loginIDStr)

	ul, err := sl.User.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		RemoteName: username,
		RemoteProfile: status.RemoteProfile{
			Name: username,
		},
		Metadata: &LoginMetadata{
			RemoteUserID: username,
			AccessToken:  password,
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict: true,
	})
	if err != nil {
		sl.Log.Err(err).Msg("Failed to create user login entry")
		return nil, fmt.Errorf("failed to create user login: %w", err)
	}

	sl.Log.Info().Str("login_id", string(ul.ID)).Msg("Successfully 'logged in' and created user login")

	err = sl.Main.LoadUserLogin(ctx, ul)
	if err != nil {
		sl.Log.Err(err).Msg("Failed to load user login after creation (this might indicate an issue)")
	}

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       LoginStepIDComplete,
		Instructions: fmt.Sprintf("Successfully logged in as '%s'", username),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

// Cancel implements bridgev2.LoginProcessUserInput.
func (sl *SimpleLogin) Cancel() {
	sl.Log.Debug().Msg("Login process cancelled")
}
