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

	"github.com/williamkray/mrtc-sip-bridge/pkg/calls"
	"github.com/williamkray/mrtc-sip-bridge/pkg/core"
	"github.com/williamkray/mrtc-sip-bridge/pkg/hostkit"
	"github.com/williamkray/mrtc-sip-bridge/pkg/sipaccount"
	"github.com/williamkray/mrtc-sip-bridge/pkg/sipua"
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

	rtc *core.Ingest

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
}

func newCallsExt(c *MyConnector) connectorExtension {
	x := &callsExt{
		c:             c,
		accounts:      map[networkid.UserLoginID]*loginCalls{},
		loginBusy:     map[networkid.UserLoginID]bool{},
		callRoomIndex: map[id.RoomID]networkid.PortalKey{},
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
	x.enabled = x.cfg.Enabled
	if !x.enabled {
		x.log.Info().Msg("SIP calls are disabled (network.calls.enabled is not set)")
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
	core.RegisterMatrixRTCHandlers(mc.EventProcessor, hostkit.ResolveRTCLogin(br, x.hasAccount), x.rtc)
	x.log.Info().Msg("SIP call support enabled (set up an account with `sip-login` to register)")
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
	if x.encryptionDefault() {
		x.log.Warn().Msg("matrix.encryption.default is enabled: SIP calls only ring in unencrypted rooms (Element Call would use SFrame E2EE in encrypted ones)")
	}
	netCfg := x.hkCfg.NetConfig()
	netCfg.KeyPrefix = "network.calls."
	rt, err := sipaccount.NewRuntime(ctx, netCfg, x.log)
	if err != nil {
		return fmt.Errorf("start SIP runtime: %w", err)
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

// encryptionDefault reports matrix.encryption.default of the bridge config.
func (x *callsExt) encryptionDefault() bool {
	if x.br == nil {
		return false
	}
	mc, ok := x.br.Matrix.(*matrix.Connector)
	if !ok || mc == nil || mc.Config == nil {
		return false
	}
	return mc.Config.Encryption.Default
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
		return mgr
	}
	return nil
}

func (x *callsExt) commandPrefix() string {
	if x.br != nil && x.br.Config != nil && x.br.Config.CommandPrefix != "" {
		return x.br.Config.CommandPrefix
	}
	return "!jmpchat"
}
