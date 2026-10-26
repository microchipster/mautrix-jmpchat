//go:build sipcalls

// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

	"github.com/williamkray/mrtc-sip-bridge/pkg/core"
	"github.com/williamkray/mrtc-sip-bridge/pkg/sipaccount"
	"github.com/williamkray/mrtc-sip-bridge/pkg/sipua"
)

// loginMeta returns the JMP.chat login metadata of a bridge login.
func loginMeta(ul *bridgev2.UserLogin) *LoginMetadata {
	if ul == nil {
		return nil
	}
	meta, _ := ul.Metadata.(*LoginMetadata)
	return meta
}

// sipCreds converts stored SIP credentials, reporting whether a complete account exists.
func sipCreds(meta *LoginMetadata) (sipaccount.Credentials, bool) {
	if meta == nil || meta.SIPCalls == nil {
		return sipaccount.Credentials{}, false
	}
	creds := sipaccount.Credentials{
		Username:      meta.SIPCalls.Username,
		Password:      meta.SIPCalls.Password,
		Registrar:     meta.SIPCalls.Registrar,
		Domain:        meta.SIPCalls.Domain,
		Transport:     meta.SIPCalls.Transport,
		DisplayNumber: meta.SIPCalls.DisplayNumber,
	}
	if !creds.Complete() {
		return sipaccount.Credentials{}, false
	}
	return creds, true
}

// setSIPCreds stores SIP credentials in the login metadata (no-op when meta is nil).
func setSIPCreds(meta *LoginMetadata, creds sipaccount.Credentials) {
	if meta == nil {
		return
	}
	meta.SIPCalls = &SIPCallCredentials{
		Username:      creds.Username,
		Password:      creds.Password,
		Registrar:     creds.Registrar,
		Domain:        creds.Domain,
		Transport:     creds.Transport,
		DisplayNumber: creds.DisplayNumber,
	}
}

// clearSIPCreds removes stored SIP credentials.
func clearSIPCreds(meta *LoginMetadata) {
	if meta != nil {
		meta.SIPCalls = nil
	}
}

// ensureAccountForLogin starts a login's SIP account when credentials are stored and no account
// exists yet.
func (x *callsExt) ensureAccountForLogin(ul *bridgev2.UserLogin) {
	creds, ok := sipCreds(loginMeta(ul))
	if !ok {
		return
	}
	x.mu.Lock()
	existing := x.accounts[ul.ID]
	stopped := x.stopped
	x.mu.Unlock()
	if existing != nil || stopped {
		return
	}
	acct := x.newLoginAccount(ul, creds)
	if acct == nil {
		return
	}
	if !x.attachAccount(ul, acct) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		acct.Stop(ctx)
		cancel()
		return
	}
	x.startAsync(ul, acct)
}

// newLoginAccount builds a SIP account for a login (nil when the runtime is not up yet).
func (x *callsExt) newLoginAccount(ul *bridgev2.UserLogin, creds sipaccount.Credentials) *sipaccount.Account {
	rt := x.runtime()
	if rt == nil || ul == nil {
		return nil
	}
	return rt.NewAccount(sipaccount.AccountConfig{
		Creds:    creds,
		Log:      x.log.With().Str("login", string(ul.ID)).Logger(),
		OnState:  x.onState(ul.ID),
		OnInvite: x.onInvite(ul.ID, creds.DisplayNumber),
	})
}

// attachAccount installs an account in the accounts map. It reports false when another account is
// already installed for the login or when calls are shutting down.
func (x *callsExt) attachAccount(ul *bridgev2.UserLogin, acct *sipaccount.Account) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.stopped || ul == nil || acct == nil {
		return false
	}
	if existing := x.accounts[ul.ID]; existing != nil {
		return existing.acct == acct
	}
	x.accounts[ul.ID] = &loginCalls{id: ul.ID, ul: ul, acct: acct}
	return true
}

// detachAccount removes and returns the login's account (nil when there is none).
func (x *callsExt) detachAccount(loginID networkid.UserLoginID) *sipaccount.Account {
	x.mu.Lock()
	defer x.mu.Unlock()
	lc := x.accounts[loginID]
	if lc == nil {
		return nil
	}
	delete(x.accounts, loginID)
	return lc.acct
}

// shutdownAccount ends the login's calls in the manager and stops the SIP account.
func (x *callsExt) shutdownAccount(ctx context.Context, loginID networkid.UserLoginID, acct *sipaccount.Account) {
	if acct == nil {
		return
	}
	if mgr := x.manager(); mgr != nil {
		mgr.EndLogin(ctx, core.LoginRef(loginID))
	}
	acct.Stop(ctx)
}

// startAsync starts a SIP account in the background.
func (x *callsExt) startAsync(ul *bridgev2.UserLogin, acct *sipaccount.Account) {
	x.teardown.Add(1)
	go func() {
		defer x.teardown.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := acct.Start(ctx); err != nil {
			x.log.Warn().Err(err).Str("login_id", string(ul.ID)).Msg("Failed to start the SIP account")
		}
	}()
}

// onState tracks registration state and reports rejected credentials once per login.
func (x *callsExt) onState(loginID networkid.UserLoginID) func(sipua.StateEvent) {
	return func(ev sipua.StateEvent) {
		notify := false
		x.mu.Lock()
		if lc := x.accounts[loginID]; lc != nil {
			switch ev.State {
			case sipua.StateRegistered:
				lc.registered = true
				lc.authNoted = false
			default:
				lc.registered = false
				if ev.State == sipua.StateAuthFailed && !lc.authNoted {
					lc.authNoted = true
					notify = true
				}
			}
		}
		x.mu.Unlock()
		switch ev.State {
		case sipua.StateRegistered:
			x.log.Info().Str("login_id", string(loginID)).Msg("SIP account registered")
		case sipua.StateAuthFailed:
			x.log.Warn().Int("sip_status", ev.StatusCode).Str("login_id", string(loginID)).Msg("SIP registration was rejected")
			if notify {
				x.notifyAuthFailure(loginID, ev)
			}
		}
	}
}

// notifyAuthFailure posts a management-room notice about rejected SIP credentials.
// notifyAuthFailure tells the owner that SIP registration credentials were rejected.
func (x *callsExt) notifyAuthFailure(loginID networkid.UserLoginID, ev sipua.StateEvent) {
	x.notifyManagement(loginID, fmt.Sprintf("SIP registration for %s was rejected (SIP %d). Fix the password with `%s sip-login`.", loginID, ev.StatusCode, x.commandPrefix()))
}

// notifyManagement sends a notice to the login owner's management room (no-op when unknown).
func (x *callsExt) notifyManagement(loginID networkid.UserLoginID, text string) {
	roomID := x.managementRoom(loginID)
	if roomID == "" || x.br == nil {
		return
	}
	sendNotice(x.br.Bot, roomID, text)
}

// managementRoom returns the management room of the login's owner ("" when unknown).
func (x *callsExt) managementRoom(loginID networkid.UserLoginID) id.RoomID {
	if x.br == nil {
		return ""
	}
	ul := x.br.GetCachedUserLoginByID(loginID)
	if ul == nil || ul.User == nil {
		return ""
	}
	return id.RoomID(ul.User.ManagementRoom)
}

// onInvite accepts an inbound call for the login (or rejects it when calls are not running).
func (x *callsExt) onInvite(loginID networkid.UserLoginID, localE164 string) func(sipua.InboundCall) {
	return func(call sipua.InboundCall) {
		acct := x.account(loginID)
		mgr := x.manager()
		if acct == nil || mgr == nil || !acct.Running() {
			call.Reject(480, "Temporarily Unavailable")
			return
		}
		mgr.HandleInvite(core.LoginRef(loginID), localE164, call)
	}
}
