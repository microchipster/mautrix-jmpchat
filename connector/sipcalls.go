//go:build sipcalls

// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"crypto/sha256"
	"github.com/williamkray/mrtc-sip-bridge/pkg/calls"
	"github.com/williamkray/mrtc-sip-bridge/pkg/core"
	"github.com/williamkray/mrtc-sip-bridge/pkg/hostkit"
	"github.com/williamkray/mrtc-sip-bridge/pkg/matrixrtc"
	"github.com/williamkray/mrtc-sip-bridge/pkg/sipaccount"
	"github.com/williamkray/mrtc-sip-bridge/pkg/sipua"
	"github.com/williamkray/mrtc-sip-bridge/pkg/widget"
	"go.mau.fi/util/dbutil"
	"jmpchat/pkg/callcrypto"
	"maunium.net/go/mautrix/crypto"
	"strings"
)

func init() {
	registerExtension = newCallsExt
}

// sipRegisterTimeout bounds one synchronous REGISTER attempt from the sip-login command.
const sipRegisterTimeout = 30 * time.Second

// loginCalls is one login's SIP account and its registration state.
type loginCalls struct {
	id         networkid.UserLoginID
	ul         *bridgev2.UserLogin
	acct       *sipaccount.Account
	registered bool
	authNoted  bool
}

// callsExt is the SIP calls extension: it owns the process-wide SIP runtime, the call manager and
// one SIP account per bridge login. It only exists in the `sipcalls` build.
type callsExt struct {
	c   *MyConnector
	br  *bridgev2.Bridge
	log zerolog.Logger

	cfg     CallsConfig
	hkCfg   hostkit.Config
	cfgErr  error
	enabled bool

	rtc  *core.Ingest
	keys *callcrypto.Manager

	mu        sync.Mutex
	saveMu    sync.Mutex
	rt        *sipaccount.Runtime
	calls     *calls.Manager
	accounts  map[networkid.UserLoginID]*loginCalls
	loginBusy map[networkid.UserLoginID]bool
	stopped   bool
	teardown  sync.WaitGroup

	// callRoomMu guards callRoomIndex, a cache of linked call rooms (Matrix room -> portal key).
	callRoomMu    sync.Mutex
	callRoomIndex map[id.RoomID]networkid.PortalKey

	// callRoomWarmMu guards callRoomWarmed, which ensures the call-room index is loaded from
	// portal metadata at most once per process.
	callRoomWarmMu sync.Mutex
	callRoomWarmed bool

	// filteredNoticeMu guards filteredNotices, the rate limiter for the
	// management notices about rejected roster-only callers.
	filteredNoticeMu sync.Mutex
	filteredNotices  map[string]time.Time

	// Dialpad widget state (see sipcalls_widget.go).
	widgetSrv  *widget.Server
	widgetInst *widget.Installer
	widgetStop context.CancelFunc
	widgetWG   sync.WaitGroup
}

func newCallsExt(c *MyConnector) connectorExtension {
	x := &callsExt{
		c:               c,
		accounts:        map[networkid.UserLoginID]*loginCalls{},
		loginBusy:       map[networkid.UserLoginID]bool{},
		callRoomIndex:   map[id.RoomID]networkid.PortalKey{},
		filteredNotices: map[string]time.Time{},
	}
	base := zerolog.Nop()
	if c != nil {
		base = c.log
	}
	x.log = base.With().Str("component", "calls").Logger()
	return x
}

// setCallRoomIndex caches the portal key of a linked call room.
func (x *callsExt) setCallRoomIndex(roomID id.RoomID, key networkid.PortalKey) {
	if roomID == "" {
		return
	}
	x.callRoomMu.Lock()
	defer x.callRoomMu.Unlock()
	if x.callRoomIndex == nil {
		x.callRoomIndex = map[id.RoomID]networkid.PortalKey{}
	}
	x.callRoomIndex[roomID] = key
}

// purgeCallRoomIndex drops a linked call room from the cache.
func (x *callsExt) purgeCallRoomIndex(roomID id.RoomID) {
	x.callRoomMu.Lock()
	defer x.callRoomMu.Unlock()
	delete(x.callRoomIndex, roomID)
}

// cachedCallRoomPortal looks a call room up in the cache.
func (x *callsExt) cachedCallRoomPortal(roomID id.RoomID) (networkid.PortalKey, bool) {
	x.callRoomMu.Lock()
	defer x.callRoomMu.Unlock()
	key, ok := x.callRoomIndex[roomID]
	return key, ok
}

func (x *callsExt) registerCommands(proc *commands.Processor) {
	if proc == nil {
		return
	}
	registerSIPCommands(x, proc)
}

// postInit runs from the main PostInit hook, before the bridge starts: it validates the config and
// registers the MatrixRTC event handlers while br.Matrix's event processor is still stopped.
func (x *callsExt) postInit(br *bridgev2.Bridge, proc *commands.Processor) {
	x.br = br
	if br == nil {
		x.cfgErr = fmt.Errorf("calls extension started without a bridge")
		return
	}
	if x.c != nil && x.c.config != nil {
		x.cfg = x.c.config.Calls
	}
	x.enabled = x.cfg.IsEnabled()
	if !x.enabled {
		x.log.Info().Msg("SIP calls are disabled (network.calls.enabled is false)")
		return
	}
	hk := x.cfg.toHostkit()
	hk.ApplyDefaults()
	if err := hk.Validate("network.calls."); err != nil {
		x.cfgErr = err
		x.log.Error().Err(err).Msg("Invalid network.calls configuration; calls will not start")
		return
	}
	x.hkCfg = hk
	mc, ok := br.Matrix.(*matrix.Connector)
	if !ok || mc == nil || mc.EventProcessor == nil {
		x.cfgErr = fmt.Errorf("matrix connector is not available for MatrixRTC handlers")
		x.log.Error().Msg("Matrix event processor is not available; calls will not start")
		return
	}
	x.rtc = core.NewIngest(core.LogSink{Log: &x.log})
	x.rtc.SetLocalNumber(x.loginNumber)
	x.keys = callcrypto.NewManager(callcrypto.ManagerOptions{
		Log:    x.log.With().Str("component", "callcrypto").Logger(),
		Device: x.callDeviceProvider(mc),
	})
	mc.EventProcessor.On(event.ToDeviceEncrypted, func(ctx context.Context, evt *event.Event) {
		x.keys.HandleToDevice(ctx, evt)
	})
	// Register the MatrixRTC handlers ourselves instead of core.RegisterMatrixRTCHandlers so a
	// dispatch cannot miss because of the event's TypeClass: unstable event types are absent from
	// the mautrix GuessClass table, and the class an event carries at dispatch time depends on
	// the path it took (timeline vs decrypted copy). The core only cares about the event type, so
	// register the same handler for every plausible class and let the parser reject the rest.
	resolve := x.resolveRTCLogin(br)
	for _, ingType := range matrixrtc.IngestEventTypes() {
		for _, class := range []event.TypeClass{event.StateEventType, event.MessageEventType, event.UnknownEventType} {
			handlerType := ingType
			handlerType.Class = class
			mc.EventProcessor.On(handlerType, func(ctx context.Context, evt *event.Event) {
				login, ok := resolve(ctx, evt)
				if x.log.GetLevel() <= zerolog.DebugLevel {
					x.log.Debug().
						Str("rtc_event_type", evt.Type.String()).
						Str("rtc_event_class", evt.Type.Class.Name()).
						Bool("rtc_resolved", ok).
						Stringer("event_id", evt.ID).
						Msg("MatrixRTC event handed to the call core")
				}
				x.rtc.HandleMatrixRTCEvent(ctx, login, evt)
				if x.keys != nil {
					if parsed, ok := matrixrtc.FromMatrixEvent(evt); ok {
						if mem, ok := matrixrtc.ParseMembership(parsed, time.Now()); ok {
							x.keys.HandleMemberships(ctx, evt.RoomID, []matrixrtc.Membership{mem})
						}
					}
				}
			})
		}
	}
	registerCallCommands(x, proc)
	x.log.Info().Msg("SIP call support enabled (set up an account with `sip-login` to register)")
}

// resolveRTCLogin resolves MatrixRTC membership events to the login whose call owns the room.
// Linked call rooms are stored in portal metadata (PortalMetadata.CallRoomMXID), not as portal
// rows, so the stock resolver built on br.GetPortalByMXID cannot see them and the call core would
// silently drop every membership event. This resolver checks the call-room cache first and falls
// back to the stock resolver for ordinary portal rooms. The cache is in-memory only, so a lookup
// miss warms it once per process from portal metadata.
func (x *callsExt) resolveRTCLogin(br *bridgev2.Bridge) core.LoginResolver {
	fallback := hostkit.ResolveRTCLogin(br, x.hasAccount)
	return func(ctx context.Context, evt *event.Event) (core.LoginRef, bool) {
		if evt == nil || br == nil {
			return "", false
		}
		if br.IsGhostMXID(evt.Sender) || evt.Sender == br.Bot.GetMXID() {
			return "", false
		}
		x.log.Debug().
			Str("rtc_sender", evt.Sender.String()).
			Str("rtc_room", evt.RoomID.String()).
			Msg("resolving MatrixRTC event")
		if login, ok := x.loginForCallRoom(ctx, evt.RoomID); ok {
			return login, true
		}
		x.warmCallRoomIndex(ctx)
		if login, ok := x.loginForCallRoom(ctx, evt.RoomID); ok {
			return login, true
		}
		if login, ok := fallback(ctx, evt); ok {
			return login, true
		}
		// The stock resolver cannot see the owning login of a portal room in single-login
		// deployments: bridgev2 leaves the portal receiver empty and such rooms are not in the
		// call-room index. Resolve them the way loginForCallRoom does (the only login with a SIP
		// account), so answer memberships, declines and outbound triggers are not dropped.
		portal, err := br.GetPortalByMXID(ctx, evt.RoomID)
		if err != nil || portal == nil {
			x.log.Debug().Err(err).Str("rtc_room", evt.RoomID.String()).Msg("MatrixRTC portal lookup failed")
			return "", false
		}
		if login, ok := x.portalCallLogin(portal.Receiver); ok {
			x.log.Debug().Str("rtc_login_id", string(login)).Msg("resolved MatrixRTC event via the portal")
			return login, true
		}
		x.log.Debug().Str("rtc_room", evt.RoomID.String()).Msg("MatrixRTC event in a room without a SIP login")
		return "", false
	}
}

// portalCallLogin resolves the login that owns calls in a portal room from the portal's receiver,
// falling back to the only login with a SIP account: single-login deployments leave the receiver
// empty, and such rooms are not in the call-room index either (loginForCallRoom falls back the
// same way for the calls it hosts).
func (x *callsExt) portalCallLogin(receiver networkid.UserLoginID) (core.LoginRef, bool) {
	loginID := receiver
	if loginID == "" {
		loginID = x.singleAccountLogin()
	}
	if loginID == "" || !x.hasAccount(loginID) {
		return "", false
	}
	return core.LoginRef(loginID), true
}

// loginForCallRoom maps a linked call room to the login that should receive its call events.
func (x *callsExt) loginForCallRoom(ctx context.Context, roomID id.RoomID) (core.LoginRef, bool) {
	key, ok := x.cachedCallRoomPortal(roomID)
	x.log.Debug().Bool("rtc_index_hit", ok).Str("rtc_room", roomID.String()).Msg("call-room index lookup")
	if !ok {
		return "", false
	}
	portal, err := x.br.GetExistingPortalByKey(ctx, key)
	if err != nil {
		x.log.Debug().Err(err).Msg("call-room portal load failed")
		return "", false
	}
	if portal == nil {
		x.log.Debug().Msg("call-room portal missing")
		return "", false
	}
	// Single-login bridges leave the portal receiver empty: the owning login is then simply the
	// only login with a SIP account. This must be the same core.LoginRef value that onInvite
	// passes to HandleInvite (the UserLoginID). Multi-login deployments address portals by
	// receiver, so prefer that when set.
	loginID := portal.Receiver
	if loginID == "" {
		loginID = x.singleAccountLogin()
	}
	hasAccount := loginID != "" && x.hasAccount(loginID)
	x.log.Debug().
		Str("rtc_portal_receiver", string(portal.Receiver)).
		Str("rtc_login_id", string(loginID)).
		Bool("rtc_has_account", hasAccount).
		Msg("call-room portal loaded")
	if !hasAccount {
		return "", false
	}
	return core.LoginRef(loginID), true
}

// singleAccountLogin returns the only login that has a SIP account, or "" when there is none or
// more than one (multi-login deployments always address portals by receiver instead).
func (x *callsExt) singleAccountLogin() networkid.UserLoginID {
	x.mu.Lock()
	defer x.mu.Unlock()
	var only networkid.UserLoginID
	for loginID, lc := range x.accounts {
		if lc == nil || lc.acct == nil {
			continue
		}
		if only != "" {
			return ""
		}
		only = loginID
	}
	return only
}

// warmCallRoomIndex loads linked call rooms from portal metadata into the in-memory cache. The
// cache is not persisted and cmdCallRoom only fills it in the running process, so after a restart
// the first membership event for a call room would otherwise be dropped. Runs at most once per
// process (a retry is allowed if listing portals fails).
func (x *callsExt) warmCallRoomIndex(ctx context.Context) {
	x.callRoomWarmMu.Lock()
	if x.callRoomWarmed {
		x.callRoomWarmMu.Unlock()
		return
	}
	x.callRoomWarmed = true
	x.callRoomWarmMu.Unlock()
	portals, err := x.br.GetAllPortals(ctx)
	if err != nil {
		x.log.Warn().Err(err).Msg("Failed to list portals while warming the call-room index")
		x.callRoomWarmMu.Lock()
		x.callRoomWarmed = false
		x.callRoomWarmMu.Unlock()
		return
	}
	for _, portal := range portals {
		if portal == nil {
			continue
		}
		meta, ok := portal.Metadata.(*PortalMetadata)
		if !ok || meta == nil || meta.CallRoomMXID == "" {
			continue
		}
		x.setCallRoomIndex(meta.CallRoomMXID, networkid.PortalKey{ID: portal.ID, Receiver: portal.Receiver})
	}
	x.log.Debug().Int("rtc_portals", len(portals)).Msg("call-room index warmed")
}

// start brings up the SIP runtime and call manager. It runs after br.Matrix.Start, so postInit
// already installed the event handlers.
func (x *callsExt) start(ctx context.Context) error {
	if !x.enabled {
		return nil
	}
	if x.cfgErr != nil {
		return x.cfgErr
	}
	netCfg := x.hkCfg.NetConfig()
	netCfg.KeyPrefix = "network.calls."
	rt, err := sipaccount.NewRuntime(ctx, netCfg, x.log)
	if err != nil {
		if x.cfg.Enabled != nil && *x.cfg.Enabled {
			return fmt.Errorf("start SIP runtime: %w", err)
		}
		// Calling is available by default, so a runtime failure must not stop
		// the whole bridge from starting.
		x.log.Error().Err(err).Msg("Failed to start the SIP runtime; calls are disabled for this session")
		return nil
	}
	mgr, _ := hostkit.NewCallStack(x.br, &x.hkCfg, hostkit.CallStackDeps{
		Host:        x.callHost(),
		Dial:        x.dial,
		LoginNumber: x.loginNumber,
		Log:         x.log,
	})
	x.mu.Lock()
	x.rt = rt
	x.calls = mgr
	x.mu.Unlock()
	if x.rtc != nil {
		x.rtc.SetSink(mgr)
	}
	// Start the optional Dialpad widget now that the call stack exists.
	x.startWidget(ctx)
	// Start SIP accounts for logins that already existed when the bridge started.
	for _, ul := range x.br.GetAllCachedUserLogins() {
		x.ensureAccountForLogin(ul)
	}
	return nil
}

// stop tears everything down: pending account starts, the accounts themselves, the manager and
// the process-wide SIP runtime.
func (x *callsExt) stop() {
	if !x.enabled {
		return
	}
	x.mu.Lock()
	if x.stopped {
		x.mu.Unlock()
		return
	}
	x.stopped = true
	mgr := x.calls
	rt := x.rt
	accounts := make([]*loginCalls, 0, len(x.accounts))
	for _, lc := range x.accounts {
		accounts = append(accounts, lc)
	}
	x.accounts = map[networkid.UserLoginID]*loginCalls{}
	x.mu.Unlock()

	done := make(chan struct{})
	go func() {
		x.teardown.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		x.log.Warn().Msg("Timed out waiting for SIP account teardown")
	}

	// Stop the Dialpad widget before tearing down the call stack it depends on.
	x.stopWidget()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, lc := range accounts {
		x.shutdownAccount(ctx, lc.id, lc.acct)
	}
	if mgr != nil {
		mgr.Stop(ctx)
	}
	if rt != nil {
		rt.Close()
	}
}

// loginLoaded starts the login's SIP account when credentials are stored.
func (x *callsExt) loginLoaded(ul *bridgev2.UserLogin, client *MyNetworkClient) {
	if !x.enabled || ul == nil {
		return
	}
	x.ensureAccountForLogin(ul)
}

// loginStopped stops the login's SIP account asynchronously.
func (x *callsExt) loginStopped(client *MyNetworkClient) {
	if !x.enabled || client == nil || client.login == nil {
		return
	}
	acct := x.detachAccount(client.login.ID)
	if acct == nil {
		return
	}
	loginID := client.login.ID
	x.teardown.Add(1)
	go func() {
		defer x.teardown.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		x.shutdownAccount(ctx, loginID, acct)
	}()
}

// decorateChatInfo marks call rooms with the power level overrides Element Call needs.
func (x *callsExt) decorateChatInfo(portalID string, info *bridgev2.ChatInfo) {
	if !x.enabled || info == nil || info.Members == nil {
		return
	}
	if _, ok := e164FromCheogramJID(portalID); !ok {
		return
	}
	overrides := hostkit.CallPowerLevelOverrides()
	if overrides == nil {
		return
	}
	if info.Members.PowerLevels == nil {
		info.Members.PowerLevels = overrides
		return
	}
	if info.Members.PowerLevels.Events == nil {
		info.Members.PowerLevels.Events = make(map[event.Type]int, len(overrides.Events))
	}
	for evtType, level := range overrides.Events {
		if _, exists := info.Members.PowerLevels.Events[evtType]; !exists {
			info.Members.PowerLevels.Events[evtType] = level
		}
	}
}

// suppressCallNotice drops XEP-0353 notices for a peer whose calls already ring through SIP.
func (x *callsExt) suppressCallNotice(loginID networkid.UserLoginID, peerBareJID string) bool {
	if !x.enabled {
		return false
	}
	if _, ok := e164FromCheogramJID(peerBareJID); !ok {
		return false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	lc := x.accounts[loginID]
	return lc != nil && lc.acct != nil && lc.registered
}

// --- accessors used by the accounts/commands/host files ---

func (x *callsExt) runtime() *sipaccount.Runtime {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.rt
}

func (x *callsExt) manager() *calls.Manager {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.calls
}

func (x *callsExt) account(loginID networkid.UserLoginID) *sipaccount.Account {
	x.mu.Lock()
	defer x.mu.Unlock()
	if lc := x.accounts[loginID]; lc != nil {
		return lc.acct
	}
	return nil
}

func (x *callsExt) hasAccount(loginID networkid.UserLoginID) bool {
	return x.account(loginID) != nil
}

// loginNumber returns the E.164 number of a login's SIP account ("" when there is none).
func (x *callsExt) loginNumber(ref core.LoginRef) string {
	acct := x.account(networkid.UserLoginID(ref))
	if acct == nil {
		return ""
	}
	return acct.Creds().DisplayNumber
}

// dial places an outbound call on the login's SIP account.
func (x *callsExt) dial(ref core.LoginRef, req sipua.DialRequest) (sipua.OutboundCall, error) {
	acct := x.account(networkid.UserLoginID(ref))
	if acct == nil {
		return nil, sipua.ErrNotRegistered
	}
	return acct.Dial(req)
}

// callAPI exposes the call manager for the hostkit commands. It returns a nil interface (never a
// typed nil) when calls have not started.
func (x *callsExt) callAPI() hostkit.CallAPI {
	if mgr := x.manager(); mgr != nil {
		return &callAPIProxy{x: x, mgr: mgr}
	}
	return nil
}

func (x *callsExt) commandPrefix() string {
	if x.br != nil && x.br.Config != nil && x.br.Config.CommandPrefix != "" {
		return x.br.Config.CommandPrefix
	}
	return "!jmpchat"
}

// callDeviceProvider builds the call E2E device for a ghost: a stable Matrix device that
// holds the Olm account used for call key distribution. Shared across all calls of the ghost.
func (x *callsExt) callDeviceProvider(mc *matrix.Connector) callcrypto.DeviceProvider {
	return func(ctx context.Context, userID id.UserID) (callcrypto.Device, error) {
		cli := mc.AS.Client(userID)
		if cli.DeviceID != callcrypto.CallDeviceID {
			if err := cli.CreateDeviceMSC4190(ctx, callcrypto.CallDeviceID, callcrypto.CallDeviceName); err != nil {
				return callcrypto.Device{}, fmt.Errorf("create the call device: %w", err)
			}
		}
		log := x.log.With().Str("component", "callcrypto").Str("user_id", userID.String()).Logger()
		store := crypto.NewSQLCryptoStore(x.br.DB.Database, dbutil.ZeroLogger(log),
			userID.String()+":"+string(callcrypto.CallDeviceID), callcrypto.CallDeviceID, x.callPickleKey(mc))
		if err := store.DB.Upgrade(ctx); err != nil {
			return callcrypto.Device{}, fmt.Errorf("upgrade the call crypto store: %w", err)
		}
		mach := crypto.NewOlmMachine(cli, &log, store, mc.StateStore)
		if err := mach.Load(ctx); err != nil {
			return callcrypto.Device{}, fmt.Errorf("load the call crypto machine: %w", err)
		}
		if err := mach.ShareKeys(ctx, -1); err != nil {
			log.Warn().Err(err).Msg("Failed to share the call device keys")
		}
		return callcrypto.Device{Client: cli, Machine: mach}, nil
	}
}

// callPickleKey returns the key protecting the stored call-device Olm accounts. An explicit
// pickle_key is hashed; otherwise the appservice token is used, so the key is stable and
// never stored next to the database.
func (x *callsExt) callPickleKey(mc *matrix.Connector) []byte {
	secret := strings.TrimSpace(x.cfg.PickleKey)
	if secret == "" && mc.AS != nil && mc.AS.Registration != nil {
		secret = mc.AS.Registration.AppToken
	}
	sum := sha256.Sum256([]byte("jmpchat-call-e2ee:" + secret))
	return sum[:]
}
