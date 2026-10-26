//go:build sipcalls

// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

	"github.com/williamkray/mrtc-sip-bridge/pkg/hostkit"
	"github.com/williamkray/mrtc-sip-bridge/pkg/widget"
)

var errWidgetNotRunning = errors.New("the Dialpad widget is not running")

// widgetInstaller adds the sweep hook to the hostkit installer, so call rooms
// can be re-checked when they gain or lose members.
type widgetInstaller interface {
	hostkit.WidgetInstaller
	RequestSweep(from context.Context, trigger string)
}

// lazyWidgetInstaller resolves the running widget server on every call, so the
// commands keep working while calling is still starting up.
type lazyWidgetInstaller struct{ x *callsExt }

func (l lazyWidgetInstaller) Ensure(ctx context.Context, roomID id.RoomID, title string) error {
	inst := l.x.widgetInstaller()
	if inst == nil {
		return errWidgetNotRunning
	}
	return inst.Ensure(ctx, roomID, title)
}

func (l lazyWidgetInstaller) Enqueue(roomID id.RoomID, title string) {
	if inst := l.x.widgetInstaller(); inst != nil {
		inst.Enqueue(roomID, title)
	}
}

func (l lazyWidgetInstaller) RequestSweep(from context.Context, trigger string) {
	if inst := l.x.widgetInstaller(); inst != nil {
		inst.RequestSweep(from, trigger)
	}
}

// lazyDialpad resolves the Dialpad links API from the running widget server.
type lazyDialpad struct{ x *callsExt }

func (l lazyDialpad) links() (hostkit.DialpadLinks, error) {
	l.x.mu.Lock()
	srv := l.x.widgetSrv
	l.x.mu.Unlock()
	if srv == nil {
		return nil, errWidgetNotRunning
	}
	links := srv.DialpadLinks()
	if links == nil {
		return nil, errors.New("the Dialpad app is not enabled on this bridge")
	}
	return links, nil
}

func (l lazyDialpad) Issue(ctx context.Context, user id.UserID, login networkid.UserLoginID, room id.RoomID, name string) (hostkit.LinkIssue, error) {
	links, err := l.links()
	if err != nil {
		return hostkit.LinkIssue{}, err
	}
	return links.Issue(ctx, user, login, room, name)
}

func (l lazyDialpad) AttachNotice(ctx context.Context, linkID string, room id.RoomID, evt id.EventID) error {
	links, err := l.links()
	if err != nil {
		return err
	}
	return links.AttachNotice(ctx, linkID, room, evt)
}

func (l lazyDialpad) List(ctx context.Context, user id.UserID) ([]hostkit.LinkInfo, error) {
	links, err := l.links()
	if err != nil {
		return nil, err
	}
	return links.List(ctx, user)
}

func (l lazyDialpad) Revoke(ctx context.Context, user id.UserID, linkID string) (int, error) {
	links, err := l.links()
	if err != nil {
		return 0, err
	}
	return links.Revoke(ctx, user, linkID)
}

func (x *callsExt) widgetEnabled() bool {
	return x.enabled && x.cfgErr == nil && x.hkCfg.Widget.Enabled
}

func (x *callsExt) widgetInstaller() *widget.Installer {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.widgetInst
}

// widgetCommandInstaller returns the installer for the `widget` command, or
// nil when the Dialpad is not enabled (in which case the command is hidden).
func (x *callsExt) widgetCommandInstaller() hostkit.WidgetInstaller {
	if !x.widgetEnabled() {
		return nil
	}
	return lazyWidgetInstaller{x: x}
}

// dialpadCommandDeps returns the Dialpad links API for the dialpad commands,
// or nil when the PWA is disabled (in which case the commands are hidden).
func (x *callsExt) dialpadCommandDeps() hostkit.DialpadLinks {
	if !x.widgetEnabled() || !x.hkCfg.Widget.PWA.Enabled {
		return nil
	}
	return lazyDialpad{x: x}
}

// startWidget starts the Dialpad widget server when enabled, or schedules a
// removal sweep when the widget was disabled but still has state on rooms.
func (x *callsExt) startWidget(ctx context.Context) {
	cfg := x.hkCfg.Widget
	if !cfg.Enabled {
		if cfg.RemoveWhenDisabled {
			x.startWidgetRemoval(ctx)
		}
		return
	}
	deps := widget.Deps{
		Bridge:             x.br,
		Calls:              x.callAPI,
		ResolveRoom:        x.widgetResolveRoom,
		EligibleRooms:      x.widgetEligibleRooms,
		RoomOwners:         x.widgetRoomOwners,
		CommandPrefix:      x.commandPrefix(),
		DefaultCountryCode: x.hkCfg.DefaultCountryCode,
		Log:                x.log.With().Str("component", "widget").Logger(),
		Features:           &widgetFeatures{x: x},
	}
	if cfg.PWA.Enabled {
		store, err := widget.OpenDeviceStore(ctx, x.br, cfg, deps.Log)
		if err != nil {
			x.log.Warn().Err(err).Msg("Failed to open the Dialpad device store; the PWA stays disabled")
		} else {
			deps.Devices = store
			deps.ResolveDevice = x.widgetResolveDevice
		}
	}
	srv, err := widget.New(cfg, deps)
	if err != nil {
		x.log.Warn().Err(err).Msg("Failed to start the Dialpad widget")
		return
	}
	if err := widget.Mount(x.br, srv); err != nil {
		x.log.Warn().Err(err).Msg("Failed to mount the Dialpad widget")
		return
	}
	x.mu.Lock()
	x.widgetSrv = srv
	x.widgetInst = srv.Installer()
	x.mu.Unlock()
	srv.Start()
	x.log.Info().Msg("Dialpad widget started")
}

// startWidgetRemoval sweeps the Dialpad widget from rooms when the widget is
// disabled but was enabled before (and the config asks for cleanup).
func (x *callsExt) startWidgetRemoval(ctx context.Context) {
	rmCtx, cancel := context.WithCancel(ctx)
	x.widgetStop = cancel
	x.widgetWG.Add(1)
	go func() {
		defer x.widgetWG.Done()
		sweepCtx, sweepCancel := context.WithTimeout(rmCtx, 10*time.Minute)
		defer sweepCancel()
		deps := widget.Deps{
			Bridge:        x.br,
			EligibleRooms: x.widgetEligibleRooms,
			CommandPrefix: x.commandPrefix(),
			Log:           x.log.With().Str("component", "widget").Logger(),
		}
		if err := widget.Remove(sweepCtx, deps); err != nil && !errors.Is(err, context.Canceled) {
			x.log.Warn().Err(err).Msg("Failed to remove the Dialpad widget state")
		}
	}()
}

// stopWidget stops the widget server and waits for any scheduled sweep.
func (x *callsExt) stopWidget() {
	x.mu.Lock()
	cancel := x.widgetStop
	srv := x.widgetSrv
	x.widgetStop = nil
	x.widgetSrv = nil
	x.widgetInst = nil
	x.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if srv != nil {
		srv.Stop()
	}
	x.widgetWG.Wait()
}

func (x *callsExt) wRequirePrivate(ctx context.Context, roomID id.RoomID, owner id.UserID) error {
	ok, err := widget.PrivateRoom(ctx, x.br, roomID, owner)
	if err != nil {
		return fmt.Errorf("check room privacy: %w", err)
	}
	if !ok {
		return widget.Refuse(widget.MsgNotPrivateRoom)
	}
	return nil
}

func (x *callsExt) wDefaultLogin(user *bridgev2.User) *bridgev2.UserLogin {
	if user == nil {
		return nil
	}
	return user.GetDefaultLogin()
}

func (x *callsExt) wCachedLogin(loginID networkid.UserLoginID) *bridgev2.UserLogin {
	if loginID == "" {
		return nil
	}
	return x.br.GetCachedUserLoginByID(loginID)
}

// widgetLoginRefusal describes why a login cannot place calls right now, so
// the widget can show a helpful message instead of an empty dialpad.
func (x *callsExt) widgetLoginRefusal(ul *bridgev2.UserLogin) string {
	if x.manager() == nil {
		return "Calling is still starting; try again in a moment."
	}
	acct := x.account(ul.ID)
	if acct == nil {
		return fmt.Sprintf("No SIP account is set up for this login; run `%s sip-login`.", x.commandPrefix())
	}
	if !acct.Running() {
		return fmt.Sprintf("The SIP account is not registered right now; check `%s sip-status`.", x.commandPrefix())
	}
	return ""
}

func (x *callsExt) widgetRoomContext(kind string, ul *bridgev2.UserLogin, number string) widget.RoomContext {
	rc := widget.RoomContext{Kind: kind, Number: number}
	if ul == nil {
		rc.Refusal = "This login is not available right now."
		return rc
	}
	if refusal := x.widgetLoginRefusal(ul); refusal != "" {
		rc.Refusal = refusal
		return rc
	}
	rc.Login = ul
	return rc
}

// widgetResolveRoom maps a Matrix room to a call context for the widget: the
// management room, or a private Cheogram DM portal of the requesting user.
func (x *callsExt) widgetResolveRoom(ctx context.Context, user *bridgev2.User, roomID id.RoomID) (widget.RoomContext, error) {
	if user == nil || user.User == nil {
		return widget.RoomContext{}, widget.Refuse(widget.MsgNotBridgeRoom)
	}
	if user.User.ManagementRoom != "" && user.User.ManagementRoom == roomID {
		if err := x.wRequirePrivate(ctx, roomID, user.MXID); err != nil {
			return widget.RoomContext{}, err
		}
		return x.widgetRoomContext(widget.KindManagement, x.wDefaultLogin(user), ""), nil
	}
	portal, err := x.br.GetPortalByMXID(ctx, roomID)
	if err != nil || portal == nil {
		return widget.RoomContext{}, widget.Refuse(widget.MsgNotBridgeRoom)
	}
	var ul *bridgev2.UserLogin
	if portal.Receiver != "" {
		ul = x.wCachedLogin(portal.Receiver)
		if ul == nil || ul.UserMXID != user.MXID {
			return widget.RoomContext{}, widget.Refuse(widget.MsgNotBridgeRoom)
		}
	} else {
		ul = x.wDefaultLogin(user)
	}
	if ul == nil {
		return widget.RoomContext{}, widget.Refuse(widget.MsgNotBridgeRoom)
	}
	e164, ok := x.isCallPortal(portal, ul.ID)
	if !ok {
		return widget.RoomContext{}, widget.Refuse(widget.MsgNotBridgeRoom)
	}
	if err := x.wRequirePrivate(ctx, roomID, user.MXID); err != nil {
		return widget.RoomContext{}, err
	}
	return x.widgetRoomContext(widget.KindPortal, ul, e164), nil
}

// widgetResolveDevice resolves a PWA device login for the widget.
func (x *callsExt) widgetResolveDevice(ctx context.Context, user *bridgev2.User, loginID networkid.UserLoginID) (widget.RoomContext, error) {
	ul := x.wCachedLogin(loginID)
	if ul == nil || user == nil || user.User == nil || ul.UserMXID != user.MXID {
		return widget.RoomContext{}, widget.ErrDeviceLoginGone
	}
	return x.widgetRoomContext(widget.KindDevice, ul, ""), nil
}

// widgetRoomOwners reports who owns a room the widget is installed in.
func (x *callsExt) widgetRoomOwners(ctx context.Context, roomID id.RoomID) []id.UserID {
	portal, err := x.br.GetPortalByMXID(ctx, roomID)
	if err == nil && portal != nil && portal.Receiver != "" {
		if ul := x.wCachedLogin(portal.Receiver); ul != nil {
			return []id.UserID{ul.UserMXID}
		}
	}
	var owners []id.UserID
	for _, ul := range x.br.GetAllCachedUserLogins() {
		if ul == nil || ul.User == nil {
			continue
		}
		if ul.User.ManagementRoom == roomID {
			owners = append(owners, ul.UserMXID)
		}
	}
	return owners
}

// widgetEligibleRooms lists the rooms the widget can be installed in: the
// management rooms of every login plus every private Cheogram call room.
func (x *callsExt) widgetEligibleRooms(ctx context.Context) ([]widget.RoomRef, error) {
	var out []widget.RoomRef
	for _, ul := range x.br.GetAllCachedUserLogins() {
		if ul == nil || ul.User == nil {
			continue
		}
		if ul.User.ManagementRoom != "" {
			out = append(out, widget.RoomRef{RoomID: ul.User.ManagementRoom, Title: "Management room"})
		}
		rows, err := x.br.DB.UserPortal.GetAllForLogin(ctx, ul.UserLogin)
		if err != nil {
			continue
		}
		for _, row := range rows {
			portal, err := x.br.GetExistingPortalByKey(ctx, row.Portal)
			if err != nil || portal == nil || portal.MXID == "" {
				continue
			}
			e164, ok := x.isCallPortal(portal, ul.ID)
			if !ok {
				continue
			}
			out = append(out, widget.RoomRef{RoomID: portal.MXID, Title: "Call " + e164})
		}
	}
	return out, nil
}
