// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// connectorExtension is optional functionality compiled in behind a build tag. The only extension
// today is the SIP calling support in the `sipcalls` build (connector/sipcalls*.go). In the default
// build registerExtension stays nil and every hook is a no-op.
type connectorExtension interface {
	// registerCommands adds the extension's management-room commands.
	registerCommands(proc *commands.Processor)
	// postInit runs from the main PostInit hook, after the Matrix connector (and its event
	// processor) exists but before the bridge starts. Handlers that must observe Matrix events
	// (e.g. MatrixRTC state) have to be registered here, because br.Matrix.Start runs before
	// br.Network.Start.
	postInit(br *bridgev2.Bridge, proc *commands.Processor)
	// start brings the extension up after the bridge started. An error fails bridge startup.
	start(ctx context.Context) error
	// stop tears the extension down before the process exits.
	stop()
	// loginLoaded is called for every login whose client was (re)created.
	loginLoaded(login *bridgev2.UserLogin, client *MyNetworkClient)
	// loginStopped is called when a login's client is disconnected for good.
	loginStopped(client *MyNetworkClient)
	// decorateChatInfo may add to the chat info of a portal (e.g. call power levels for DMs).
	decorateChatInfo(portalID string, info *bridgev2.ChatInfo)
	// suppressCallNotice reports whether XEP-0353 call notices for peerBareJID should be dropped
	// because the same call is already ringing through the extension's own call path.
	suppressCallNotice(loginID networkid.UserLoginID, peerBareJID string) bool
}

// registerExtension is set by an init() in a build-tagged file; nil in the default build.
var registerExtension func(c *MyConnector) connectorExtension

// extension returns the connector's extension (created once; nil when none is compiled in).
func (c *MyConnector) extension() connectorExtension {
	if c == nil || registerExtension == nil {
		return nil
	}
	c.extnOnce.Do(func() { c.extn = registerExtension(c) })
	return c.extn
}

// extensionRegisterCommands registers the extension's commands when one is compiled in.
func (c *MyConnector) extensionRegisterCommands(proc *commands.Processor) {
	if e := c.extension(); e != nil && proc != nil {
		e.registerCommands(proc)
	}
}

// extensionPostInit lets the extension attach to the bridge before startup.
func (c *MyConnector) extensionPostInit(br *bridgev2.Bridge, proc *commands.Processor) {
	if e := c.extension(); e != nil {
		e.postInit(br, proc)
	}
}

// PostInitExtensions is called from the main PostInit hook so extensions that must register
// Matrix event handlers (SIP calls) run before br.Matrix.Start starts the event processor.
func (c *MyConnector) PostInitExtensions(br *bridgev2.Bridge) {
	if c == nil || br == nil {
		return
	}
	proc, _ := br.Commands.(*commands.Processor)
	c.extensionPostInit(br, proc)
}

// extensionStart starts the extension, if any.
func (c *MyConnector) extensionStart(ctx context.Context) error {
	if e := c.extension(); e != nil {
		return e.start(ctx)
	}
	return nil
}

// extensionStop stops the extension, if any.
func (c *MyConnector) extensionStop() {
	if e := c.extension(); e != nil {
		e.stop()
	}
}

// extensionLoginLoaded notifies the extension about a (re)loaded login.
func (c *MyConnector) extensionLoginLoaded(login *bridgev2.UserLogin, client *MyNetworkClient) {
	if e := c.extension(); e != nil {
		e.loginLoaded(login, client)
	}
}

// extensionLoginStopped notifies the extension about a disconnected login.
func (c *MyConnector) extensionLoginStopped(client *MyNetworkClient) {
	if e := c.extension(); e != nil {
		e.loginStopped(client)
	}
}

// extensionDecorateChatInfo lets the extension adjust a portal's chat info.
func (nc *MyNetworkClient) extensionDecorateChatInfo(portal *bridgev2.Portal, info *bridgev2.ChatInfo) {
	if nc == nil || nc.connector == nil || portal == nil || info == nil {
		return
	}
	if e := nc.connector.extension(); e != nil {
		e.decorateChatInfo(string(portal.ID), info)
	}
}

// extensionSuppressCallNotice reports whether the extension wants to drop XEP-0353 notices for the
// peer (its own call path handles the call).
func (nc *MyNetworkClient) extensionSuppressCallNotice(peerBareJID string) bool {
	if nc == nil || nc.connector == nil || nc.login == nil {
		return false
	}
	if e := nc.connector.extension(); e != nil {
		return e.suppressCallNotice(nc.login.ID, peerBareJID)
	}
	return false
}
