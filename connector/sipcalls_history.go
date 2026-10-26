//go:build sipcalls

// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

	"github.com/williamkray/mrtc-sip-bridge/pkg/calls"
	"github.com/williamkray/mrtc-sip-bridge/pkg/core"
	"github.com/williamkray/mrtc-sip-bridge/pkg/hostkit"
)

// callAPIProxy wraps the call manager so outgoing calls are recorded in the
// login's call history before being handed to the manager.
type callAPIProxy struct {
	x   *callsExt
	mgr hostkit.CallAPI
}

var _ hostkit.CallAPI = (*callAPIProxy)(nil)

func (p *callAPIProxy) PlaceCall(ctx context.Context, login core.LoginRef, req calls.PlaceCallRequest) (string, string, error) {
	callID, e164, err := p.mgr.PlaceCall(ctx, login, req)
	if err == nil {
		p.x.recordOutgoingCallHistory(networkid.UserLoginID(login), e164)
	}
	return callID, e164, err
}

func (p *callAPIProxy) CheckDestination(login core.LoginRef, e164 string) error {
	return p.mgr.CheckDestination(login, e164)
}

func (p *callAPIProxy) Hangup(ctx context.Context, login core.LoginRef, roomID id.RoomID) error {
	return p.mgr.Hangup(ctx, login, roomID)
}

func (p *callAPIProxy) SendDTMF(ctx context.Context, login core.LoginRef, roomID id.RoomID, digits string) (int, error) {
	return p.mgr.SendDTMF(ctx, login, roomID, digits)
}

func (p *callAPIProxy) Snapshot(login core.LoginRef) (calls.Snapshot, bool) {
	return p.mgr.Snapshot(login)
}

// recordOutgoingCallHistory stores a freshly placed outgoing call.
func (x *callsExt) recordOutgoingCallHistory(loginID networkid.UserLoginID, number string) {
	ul := x.br.GetCachedUserLoginByID(loginID)
	if ul == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		recordCallHistory(ctx, ul, CallHistoryEntry{
			Number:    number,
			Direction: "outgoing",
			Status:    "placing",
			StartedAt: time.Now(),
		})
	}()
}

// recordIncomingCallHistory stores a ringing incoming call.
func (x *callsExt) recordIncomingCallHistory(ul *bridgev2.UserLogin, number string) {
	if ul == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		recordCallHistory(ctx, ul, CallHistoryEntry{
			Number:    number,
			Direction: "incoming",
			Status:    "ringing",
			StartedAt: time.Now(),
		})
	}()
}
