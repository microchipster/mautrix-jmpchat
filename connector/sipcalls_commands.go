//go:build sipcalls

// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/williamkray/mrtc-sip-bridge/pkg/hostkit"
	"github.com/williamkray/mrtc-sip-bridge/pkg/sipaccount"
	"github.com/williamkray/mrtc-sip-bridge/pkg/sipua"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// registerSIPCommands adds the SIP account commands and, when calls are enabled, the call commands.
func registerSIPCommands(x *callsExt, proc *commands.Processor) {
	for _, handler := range []*commands.FullHandler{{
		Func:          hostkit.WithSpan("sip-login", x.cmdSIPLogin),
		Name:          "sip-login",
		RequiresLogin: true,
		Help: commands.HelpMeta{
			Section:     commands.HelpSectionAuth,
			Description: "Set up (or replace) the SIP account used for phone calls. The bridge asks for each value in turn.",
		},
	}, {
		Func:          hostkit.WithSpan("sip-logout", x.cmdSIPLogout),
		Name:          "sip-logout",
		RequiresLogin: true,
		Help: commands.HelpMeta{
			Section:     commands.HelpSectionAuth,
			Description: "Remove the SIP account of this login and stop its registration.",
		},
	}, {
		Func:          hostkit.WithSpan("sip-status", x.cmdSIPStatus),
		Name:          "sip-status",
		RequiresLogin: true,
		Help: commands.HelpMeta{
			Section:     commands.HelpSectionAuth,
			Description: "Show the SIP account and call status of this login.",
		},
	}} {
		proc.AddHandler(handler)
	}
}

// registerCallCommands adds the interactive calling surface (call/hangup/dtmf via hostkit, plus the
// call-room and roster-only-calls handlers). It must run from postInit: registerSIPCommands runs
// before postInit computes x.enabled and x.cfgErr, so gating on those values there silently skipped
// the entire surface. The individual handlers still validate state at invocation time.
func registerCallCommands(x *callsExt, proc *commands.Processor) {
	if x == nil || proc == nil || !x.enabled || x.cfgErr != nil {
		return
	}
	hostkit.RegisterCallCommands(proc, x.callCommandDeps())
	proc.AddHandler(&commands.FullHandler{
		Func:          hostkit.WithSpan("call-room", x.cmdCallRoom),
		Name:          "call-room",
		RequiresLogin: true,
		Help: commands.HelpMeta{
			Section:     commands.HelpSectionChats,
			Description: "Create (or list/remove) the legacy dedicated unencrypted call room of a phone number; only needed when `room_mode` is `call-room`, or when call encryption is off in an encrypted portal.",
		},
	})
	proc.AddHandler(&commands.FullHandler{
		Func:          hostkit.WithSpan("roster-only-calls", x.cmdRosterOnlyCalls),
		Name:          "roster-only-calls",
		RequiresLogin: true,
		Help: commands.HelpMeta{
			Section:     commands.HelpSectionChats,
			Description: "Restrict incoming SIP calls to contacts in your Jabber roster (on|off|status).",
		},
	})
}

// callCommandDeps adapts the call, hangup and dtmf commands to this host.
func (x *callsExt) callCommandDeps() hostkit.CommandDeps {
	return hostkit.CommandDeps{
		Calls: x.callAPI, // a nil interface (never a typed nil) while the manager does not exist
		Login: x.commandLoginWithAccount,
		RoomNumber: func(p *bridgev2.Portal) (string, bool) {
			if p == nil || portalUsesGroupChat(p) {
				return "", false
			}
			return e164FromCheogramJID(UnescapeJID(string(p.ID)))
		},
		RedactDTMF: x.cfg.DTMFRedactCommand != nil && *x.cfg.DTMFRedactCommand,
		Widget:     x.widgetCommandInstaller(),
		Dialpad:    x.dialpadCommandDeps(),
	}
}

// commandLoginWithAccount returns the login a call command acts on, refusing logins without a
// registered SIP account.
func (x *callsExt) commandLoginWithAccount(ce *commands.Event) (*bridgev2.UserLogin, string) {
	if ce == nil || ce.User == nil {
		return nil, "No login found."
	}
	ul, _ := selectCommandLogin(ce)
	if ul == nil {
		return nil, "No login found."
	}
	if _, ok := ul.Client.(*MyNetworkClient); !ok {
		return nil, "This command needs a JMP.chat login."
	}
	if x.hasAccount(ul.ID) {
		return ul, ""
	}
	if _, ok := sipCreds(loginMeta(ul)); ok {
		return nil, fmt.Sprintf("Your SIP account is not registered right now. Use `%s sip-status`, or `%s sip-login` to set it up again.",
			x.commandPrefix(), x.commandPrefix())
	}
	return nil, fmt.Sprintf("No SIP account is set up for this login. Use `%s sip-login`.", x.commandPrefix())
}

func (x *callsExt) cmdSIPLogin(ce *commands.Event) {
	if len(ce.Args) > 0 {
		ce.Redact() // the arguments may contain a password
		ce.Reply("Run `%s sip-login` without arguments; the bridge asks for each value.", x.commandPrefix())
		return
	}
	// Prompt answers are unprefixed: outside the management room bridgev2 would not treat them as
	// commands and the bridge would send them to the contact.
	if !ce.Sudo && (ce.User == nil || ce.RoomID != ce.User.ManagementRoom) {
		ce.Reply("Run `%s sip-login` in your management room (your DM with the bridge bot): answers typed in a chat room would be sent to the contact.", x.commandPrefix())
		return
	}
	if !x.enabled {
		ce.Reply("SIP calls are disabled on this bridge (network.calls.enabled).")
		return
	}
	if x.cfgErr != nil {
		ce.Reply("The calls configuration is invalid: %v", x.cfgErr)
		return
	}
	if x.runtime() == nil {
		ce.Reply("The bridge is still starting. Try again in a moment.")
		return
	}
	ul, _ := selectCommandLogin(ce)
	if ul == nil {
		ce.Reply("No login found.")
		return
	}
	if loginMeta(ul) == nil {
		ce.Reply("Log in to JMP.chat first.")
		return
	}
	ce.Reply("Enter your SIP account details. Answer each prompt as its own message (passwords may contain spaces). " +
		"You can create a SIP account for your JMP number in the JMP.chat account settings. Registering takes over " +
		"incoming calls for this account; if another device (desk phone, ATA) uses the same account, calls may stop " +
		"reaching it. Calls ring in the number's Cheogram room, which must be unencrypted.")
	hostkit.PromptFields(ce, "SIP login", hostkit.LoginFields(x.cfg.DefaultCountryCode),
		func(ce *commands.Event, input map[string]string) { x.submitSIPLogin(ce, ul, input) }, nil)
}

// submitSIPLogin registers the entered account and, only when the registrar accepted it, saves it.
func (x *callsExt) submitSIPLogin(ce *commands.Event, ul *bridgev2.UserLogin, input map[string]string) {
	x.mu.Lock()
	busy := x.loginBusy[ul.ID]
	if !busy {
		x.loginBusy[ul.ID] = true
	}
	x.mu.Unlock()
	if busy {
		ce.Reply("A SIP login is already in progress for this login.")
		return
	}
	defer func() {
		x.mu.Lock()
		delete(x.loginBusy, ul.ID)
		x.mu.Unlock()
	}()

	creds, err := sipaccount.ParseInput(input, x.cfg.DefaultCountryCode)
	if err != nil {
		// ParseInput errors never contain the password.
		ce.Reply("Invalid SIP account details: %v", err)
		return
	}
	meta := loginMeta(ul)
	if x.runtime() == nil || meta == nil {
		ce.Reply("The bridge is still starting. Try again in a moment.")
		return
	}
	// The previous account is stopped first: it frees its SIP port and avoids two registrations.
	prevNote := ""
	if old := x.detachAccount(ul.ID); old != nil {
		prevNote = " Your previous SIP registration was stopped."
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		x.shutdownAccount(ctx, ul.ID, old)
		cancel()
	}
	acct := x.newLoginAccount(ul, *creds)
	if acct == nil {
		ce.Reply("The bridge is still starting. Try again in a moment.")
		return
	}
	stopAccount := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		acct.Stop(ctx)
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), sipRegisterTimeout)
	defer cancel()
	ev, err := sipaccount.Register(ctx, acct, sipRegisterTimeout)
	switch sipaccount.Outcome(ev, err) {
	case "ok":
	case "auth_failed":
		stopAccount()
		ce.Reply("The registrar rejected the credentials (SIP %d %s).%s", ev.StatusCode, ev.Reason, prevNote)
		return
	case "timeout":
		stopAccount()
		ce.Reply("Timed out registering with %s.%s", creds.Registrar, prevNote)
		return
	case "transient_error":
		stopAccount()
		ce.Reply("Could not register with %s: %s.%s", creds.Registrar, ev.Reason, prevNote)
		return
	default:
		msg := "unknown error"
		if err != nil {
			msg = err.Error()
		}
		stopAccount()
		ce.Reply("Could not start the SIP client: %s.%s", msg, prevNote)
		return
	}
	// Only save the credentials once the registrar accepted them.
	x.saveMu.Lock()
	prev, hadPrev := sipCreds(meta)
	setSIPCreds(meta, *creds)
	saveErr := ul.Save(ce.Ctx)
	if saveErr != nil {
		if hadPrev {
			setSIPCreds(meta, prev)
		} else {
			clearSIPCreds(meta)
		}
	}
	x.saveMu.Unlock()
	if saveErr != nil {
		stopAccount()
		ce.Reply("Registered %s, but saving the credentials failed: %v. Run `%s sip-login` again.", creds.DisplayNumber, saveErr, x.commandPrefix())
		return
	}
	if !x.attachAccount(ul, acct) {
		stopAccount()
		ce.Reply("Registered, but the login was disconnected meanwhile; run `%s sip-login` again.", x.commandPrefix())
		return
	}
	ce.Reply("Registered %s via %s.\n\nIncoming calls to that number ring in its Cheogram room; place calls with `%s call <number>`.%s",
		creds.DisplayNumber, creds.Registrar, x.commandPrefix(), prevNote)
}

func (x *callsExt) cmdSIPLogout(ce *commands.Event) {
	ul, _ := selectCommandLogin(ce)
	if ul == nil {
		ce.Reply("No login found.")
		return
	}
	meta := loginMeta(ul)
	creds, ok := sipCreds(meta)
	acct := x.account(ul.ID)
	if !ok && acct == nil {
		ce.Reply("No SIP account is set up for this login.")
		return
	}
	number := ""
	switch {
	case ok:
		number = creds.DisplayNumber
	case acct != nil:
		number = acct.Creds().DisplayNumber
	}
	if old := x.detachAccount(ul.ID); old != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		x.shutdownAccount(ctx, ul.ID, old)
		cancel()
	}
	if ok {
		x.saveMu.Lock()
		clearSIPCreds(meta)
		err := ul.Save(ce.Ctx)
		x.saveMu.Unlock()
		if err != nil {
			ce.Reply("The SIP account was stopped, but removing it failed: %v.", err)
			return
		}
	}
	if number == "" {
		number = "that number"
	}
	ce.Reply("SIP account removed. Calls to %s no longer ring here.", number)
}

func (x *callsExt) cmdSIPStatus(ce *commands.Event) {
	ul, _ := selectCommandLogin(ce)
	if ul == nil {
		ce.Reply("No login found.")
		return
	}
	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	lines := []string{"Calls enabled: " + yesNo(x.enabled)}
	if x.cfgErr != nil {
		lines = append(lines, "Config error: "+x.cfgErr.Error())
	}
	creds, ok := sipCreds(loginMeta(ul))
	acct := x.account(ul.ID)
	if acct != nil {
		c := acct.Creds()
		creds, ok = c, true
	}
	if !ok {
		lines = append(lines, "SIP account: none")
	} else {
		transport := creds.Transport
		if transport == "" {
			transport = "udp"
		}
		lines = append(lines,
			fmt.Sprintf("SIP account: %s via %s (%s)", sipua.MaskUsername(creds.Username), creds.Registrar, transport),
			"Number: "+creds.DisplayNumber,
			"Registration: "+registrationState(acct))
	}
	ce.Reply("%s", strings.Join(lines, "\n"))
}

// registrationState describes acct's registration for sip-status (no secrets).
func registrationState(acct *sipaccount.Account) string {
	if acct == nil || !acct.Running() {
		return "stopped"
	}
	ev := acct.State()
	switch ev.State {
	case sipua.StateRegistered:
		return "registered"
	case sipua.StateAuthFailed:
		return fmt.Sprintf("auth_failed (SIP %d)", ev.StatusCode)
	case sipua.StateTransientError:
		return fmt.Sprintf("transient_error (retry in %ds)", int(ev.NextRetry.Round(time.Second)/time.Second))
	case sipua.StateStopped:
		return "stopped"
	default:
		return "registering"
	}
}

// cmdRosterOnlyCalls shows or changes whether incoming SIP calls ring only for
// contacts in the login's Jabber roster.
func (x *callsExt) cmdRosterOnlyCalls(ce *commands.Event) {
	if ce.User == nil {
		ce.Reply("Run `%s roster-only-calls` in your management room.", x.commandPrefix())
		return
	}
	if !x.enabled {
		ce.Reply("SIP calls are disabled on this bridge (network.calls.enabled).")
		return
	}
	if x.cfgErr != nil {
		ce.Reply("The calls configuration is invalid: %v", x.cfgErr)
		return
	}
	if !ce.Sudo && ce.RoomID != ce.User.ManagementRoom {
		ce.Reply("Run `%s roster-only-calls` in your management room.", x.commandPrefix())
		return
	}
	ul, _ := selectCommandLogin(ce)
	if ul == nil {
		ce.Reply("No login found.")
		return
	}
	meta := loginMeta(ul)
	if meta == nil {
		ce.Reply("Log in to JMP.chat first.")
		return
	}
	arg := ""
	if len(ce.Args) > 0 {
		arg = strings.ToLower(strings.TrimSpace(ce.Args[0]))
	}
	switch arg {
	case "", "status":
		if meta.CallRosterOnly {
			ce.Reply("Roster-only calls: **on** — calls from numbers outside your Jabber roster are blocked (the caller hears a rejection; you get a notice here).")
			if warning := x.rosterOnlyWarning(ul); warning != "" {
				ce.Reply(warning)
			}
		} else {
			ce.Reply("Roster-only calls: **off** — calls from any number ring.")
		}
	case "on", "off":
		x.saveMu.Lock()
		prev := meta.CallRosterOnly
		meta.CallRosterOnly = arg == "on"
		err := ul.Save(ce.Ctx)
		if err != nil {
			meta.CallRosterOnly = prev
		}
		x.saveMu.Unlock()
		if err != nil {
			ce.Reply("Failed to save the roster-only setting: %v", err)
			return
		}
		if arg == "on" {
			ce.Reply("Roster-only calls are now **on**. Use `%s roster-only-calls off` to ring for everyone.", x.commandPrefix())
			if warning := x.rosterOnlyWarning(ul); warning != "" {
				ce.Reply(warning)
			}
		} else {
			ce.Reply("Roster-only calls are now **off**. Calls from any number ring again.")
		}
	default:
		ce.Reply("Usage: `%s roster-only-calls [on|off|status]`", x.commandPrefix())
	}
}

// parseCallRoomArgs parses the call-room argument forms: optional --remove/--force flags and an
// optional phone number. ok=false means the usage message should be shown.
func parseCallRoomArgs(args []string) (target string, remove, force, ok bool) {
	var rest []string
	for _, arg := range args {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "--remove", "remove":
			remove = true
		case "--force", "force":
			force = true
		case "":
		default:
			rest = append(rest, arg)
		}
	}
	switch {
	case len(rest) == 0 && !remove:
		return "", false, force, true // list
	case len(rest) == 1:
		return rest[0], remove, force, true
	default:
		return "", false, false, false
	}
}

// cmdCallRoom creates, lists or removes the dedicated unencrypted call room of a number.
func (x *callsExt) cmdCallRoom(ce *commands.Event) {
	if ce == nil || ce.User == nil || x.br == nil {
		return
	}
	ctx := ce.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	target, remove, force, ok := parseCallRoomArgs(ce.Args)
	if !ok {
		ce.Reply("Usage: call-room <phone number> [--force] — create an unencrypted call room for a number\n" +
			"       call-room --remove <phone number> — unlink the call room\n" +
			"       call-room — list the linked call rooms")
		return
	}
	ul, _ := selectCommandLogin(ce)
	if ul == nil {
		ce.Reply("No login found.")
		return
	}
	if target == "" {
		x.replyCallRoomList(ce, ul)
		return
	}
	jid := normalizeJID(target)
	number, isNumber := e164FromCheogramJID(jid)
	if !isNumber {
		ce.Reply("`%s` is not a phone number. Use a number like +15551234567.", target)
		return
	}
	portalKey := networkid.PortalKey{ID: MakePortalID(jid)}
	if remove {
		portal, err := x.br.GetExistingPortalByKey(ctx, portalKey)
		if err != nil {
			ce.Reply("Failed to look up the portal: %v", err)
			return
		}
		roomID := portalCallRoomID(portal)
		if roomID == "" {
			ce.Reply("No call room is linked to %s.", number)
			return
		}
		meta, _ := portal.Metadata.(*PortalMetadata)
		meta.CallRoomMXID = ""
		portal.Metadata = meta
		if err := portal.Save(ctx); err != nil {
			ce.Reply("Failed to unlink the call room: %v", err)
			return
		}
		x.purgeCallRoomIndex(roomID)
		ce.Reply("Unlinked the call room for %s. You can leave or delete %s yourself; calls now follow the `room_mode`/`e2ee` settings again.", number, roomID.URI().MatrixToURL())
		return
	}
	portal, err := x.br.GetPortalByKey(ctx, portalKey)
	if err != nil {
		ce.Reply("Failed to load the portal: %v", err)
		return
	}
	if existing := portalCallRoomID(portal); existing != "" {
		ce.Reply("A call room is already linked for %s: %s", number, existing.URI().MatrixToURL())
		return
	}
	if portal.MXID != "" && !hostkit.RoomEncrypted(ctx, x.br, portal.MXID) && !force {
		ce.Reply("The portal room for %s is not encrypted, so calls already ring there. Use `call-room %s --force` to create a dedicated call room anyway.", number, number)
		return
	}
	roomID, err := x.createCallRoomForPortal(ctx, ul, portal, number)
	if err != nil {
		ce.Reply("Failed to create the call room: %v", err)
		return
	}
	ce.Reply("Created unencrypted call room for %s: %s\n\nThe portal room is unchanged; calls use the call room only when `room_mode` is `call-room`, or when call encryption is off in an encrypted portal.", number, roomID.URI().MatrixToURL())
}

// replyCallRoomList lists the portals that have a linked call room.
func (x *callsExt) replyCallRoomList(ce *commands.Event, ul *bridgev2.UserLogin) {
	ctx := ce.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := x.br.DB.UserPortal.GetAllForLogin(ctx, ul.UserLogin)
	if err != nil {
		ce.Reply("Failed to list the portals: %v", err)
		return
	}
	var lines []string
	for _, row := range rows {
		portal, err := x.br.GetExistingPortalByKey(ctx, row.Portal)
		if err != nil || portal == nil {
			continue
		}
		roomID := portalCallRoomID(portal)
		if roomID == "" {
			continue
		}
		number, ok := x.isCallPortal(portal, ul.ID)
		if !ok {
			number = UnescapeJID(string(portal.ID))
		}
		x.setCallRoomIndex(roomID, portal.PortalKey)
		lines = append(lines, fmt.Sprintf("- %s: %s", number, roomID.URI().MatrixToURL()))
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		ce.Reply("No call rooms are linked yet. Use `call-room <phone number>` to create one.")
		return
	}
	ce.Reply("Linked call rooms (%d):\n%s", len(lines), strings.Join(lines, "\n"))
}
