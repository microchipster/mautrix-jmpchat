<!--
SPDX-FileCopyrightText: 2026 Slavi Pantaleev

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Development notes

This document records the development progression of the bridge: what has been built,
the design considerations behind it, and the operational lessons learned along the way.
Completed items from `TODO.md` (including the adopted workstreams and the original
gateway-parity backlog) are archived here. `TODO.md` keeps only the outstanding work.

`docs/DEVEL.md` is the companion Go/codebase walkthrough for maintainers.

## Progression

### Baseline bridge build-out

The bridge began as a bridgev2 connector for XMPP networks reached through the
JMP.chat/Cheogram gateway, with the semantics that survive the phone network in mind:

- basic XMPP login and login validation (`connector/login.go`)
- DM and group portal creation/sync, MAM backfill, bookmark-driven room discovery
- text messages in both directions
- basic media/file bridging via HTTP upload/OOB/SIMS
- basic reactions in both directions
- native bridgev2 provisioning: group creation (`CreateGroup`/`GroupCreatingNetworkAPI`,
  `create-group`, `POST /v3/create_group/{type}`), identifier resolution
  (`ResolveIdentifier`, `CreateChatWithGhost`), contact list (`GetContactList`),
  `ValidateUserID`
- management-room tooling: `create`, `join-room`, prune/rebind/replace room and space
  commands, `sync-status`

### Gateway-critical parity work (P0 #1-#6)

1. **Gateway-specific UX for PSTN/SMS flows** — done phase 1. Phone-number creation/join
   flows work through the management-room `create`/`start-chat` commands and the native
   bridgev2 provisioning endpoints; `jabber:iq:gateway` discovery and prompts are covered
   by `gateway-actions` (`connector/xmpp_gateway.go`: `discoverGatewayActions`,
   `requestGatewayPrompt`, `SupportsGatewayIQ`); contact-add flows exist as
   `add-to-roster`/`save-contact` plus the CardDAV import, and gateway/service
   registration flows are reachable through `gateway-actions`, `gateway-form` and
   `gateway-submit`. Remaining work is tracked in `TODO.md`.
2. **Rich media messages with captions, subjects, and cleaner metadata** — done phase 1.
   Captions are extracted from XEP-0066/XEP-0454 attachments and merged with subject
   lines (`mergeXMPPSubjectAndBody`); downloaded and BOB media always get
   `m.file`/`m.image`/`m.audio`/`m.video` metadata (mime, size, width/height, duration)
   and voice notes are recognized (MSC1767/MSC3245); failed downloads keep a readable
   link fallback.
3. **Read receipts, chat markers, and delivery/displayed state** — done
   (`connector/xmpp_receipts.go`): inbound receipts/markers map to Matrix receipt events
   and outbound Matrix read state emits XMPP markers for supported chats; the signal
   toggles are wired through `SimpleConfig` (see WS4.7).
4. **Command UI and ad-hoc form workflows** — done phase 1: gateway/service actions are
   discoverable and submittable from the management room through `gateway-actions`,
   `gateway-form` and `gateway-submit` (`connector/xmpp_gateway.go`). Richer form UX is
   tracked in `TODO.md`.
5. **Replies and fallback quoting** — done (`connector/xmpp_reply.go`): Matrix replies
   map to `urn:xmpp:reply:0` with fallback text, and inbound XMPP replies attach Matrix
   reply relations without double-rendering quoted fallback text.
6. **Reactions parity** — done. Reaction removal is expressed as a full-set replacement
   for that sender (`currentMatrixReactionSet` + `sendXMPPReactionSet`,
   `HandleMatrixReactionRemove`); targets resolve by bridgev2 id first and then via
   persisted XMPP alias metadata (`resolveReactionTarget`, WS1.5), covering MUC and
   private-message reactions; image-based custom-emoji reactions (`mxc://`) are stored
   in Matrix but deliberately not sent to XMPP (`xmppReactionSupported`/
   `filterXMPPReactions`, tests `connector/xmpp_reaction_filter_test.go`), and an
   all-unsupported set is never sent so remote reactions are not wiped.

### Workstreams adopted from the sibling bridge

Adopted selectively from `jmpchat-matrix-bridge` (formerly `mautrix-jabber`), keeping our
SPDX headers, test style, and terse management-room UX.

#### WS1 - Messaging parity

- **WS1.1 Typing and chat states** — done in `connector/xmpp_typing.go` (outbound
  `HandleMatrixTyping` -> XEP-0085 `composing`/`paused`; inbound state parse ->
  `simplevent.Typing`; Cheogram targets suppressed). Tests in
  `connector/xmpp_typing_test.go`. Toggle: `send_typing` (WS4.7).
- **WS1.2 Corrections (XEP-0308) both ways** — done in `connector/xmpp_corrections.go`
  (outbound Matrix edit -> `<replace id=...>`; Cheogram/SMS edits sent as a new message
  prefixed `* `; inbound resolution via direct id + persisted
  `origin_id`/`xmpp_message_id`/`stanza_id` aliases; sender ownership check; media and
  unknown/foreign targets ignored). Tests in `connector/xmpp_corrections_test.go`.
- **WS1.3 Retractions (XEP-0424/0425) both ways** — done in
  `connector/xmpp_corrections.go` (outbound Matrix redaction -> XEP-0424 `<retract>`;
  Cheogram/SMS targets get a "cannot unsend" notice; inbound retractions -> Matrix
  redaction; body-less retraction stanzas bypass the empty-body guard). Outbound MUC
  retractions wait up to 3 s for the room-assigned stanza-id learned from our own
  message echo (`connector/xmpp_muc_echo.go`: `recordMucEcho`/`waitMucEcho`).
- **WS1.4 Multiple reactions per user per message** — done (verified, no new code
  needed): outbound sends the full active set; inbound reactions queue a per-sender
  `simplevent.ReactionSync` with `HasAllReactions=true`.
- **WS1.5 Reaction target resolution hardening** — done: every converted message part
  persists `XMPPMessageID`/`OriginID`/`StanzaID` (inbound `network_client.go` DBMetadata,
  outbound `handle_matrix.go` response), and `nc.resolveReactionTarget`
  (`connector/xmpp_corrections.go`) resolves XEP-0444 targets by direct bridgev2 id
  first, then via alias lookup scoped to the portal.
- **WS1.6 At-mentions both ways** — done phase 1 (XEP-0372 references): outbound Matrix
  `m.mentions` -> `<reference type="mention" uri="xmpp:<jid>"/>` and plain-body names
  rewritten to the XMPP-side name (`connector/xmpp_mentions.go`,
  `connector/matrix_mentions.go`); inbound XEP-0372 references -> Matrix `m.mentions`
  pills. Tests in `connector/xmpp_mentions_test.go`.
- **WS1.7 Message IDs, dedupe, and timestamps** — done phase 1+2: ID tiers are `id`
  attribute -> scope-qualified stanza-id (`xmp:sid:<scope>:<id>`) -> `origin-id` ->
  delay-hash (SHA-256, for history replays) -> random `xmp:r:<uuid>`
  (`connector/xmpp_msgid.go`, tests `connector/xmpp_msgid_test.go`). Trusted XEP-0203
  delays are accepted only from own server domain, the sender's domain, or the room;
  cross-path dedupe (30-day window) drops stanza-id replays and origin-id/alias replays
  of our own, but never drops contact messages on sender-chosen ids
  (`isBridgedDuplicate`, `aliasMessageIDsSince`).
- **WS1.8 Encrypted payload degradation** — done in `connector/xmpp_encrypted.go`
  (+ tests): OMEMO/OpenPGP/XEP-0380 EME/`aesgcm://` detected -> fixed `m.notice`
  ("🔒 Encrypted XMPP message (<kind>) — this bridge cannot decrypt it."), never
  downloaded, ids preserved so replies/corrections/retractions still map. OMEMO
  decryption itself stays deferred (P3 `#21`).

#### Room naming fixes (deployed user bugs)

- **Roster name import + refresh** — done in `connector/xmpp_roster.go`: XMPP roster
  `item.Name` stored into `GhostMetadata.RemoteName` (explicit `CustomName` wins) during
  roster sync; `refreshAutoNamedPortals` reruns when names change. Fixes DM room titles
  stuck on the bare phone number.
- **Initials self-heal** — done in `connector/naming.go`: a participant with an empty
  label breaks the "all named" initials path, and `portalLooksAutoNamed` treats
  all-uppercase initials-like names as auto-named so short/stale initials regenerate.
- **Special characters in contact names** — audited + hardened: `normalizeContactName`
  trims, collapses whitespace and drops control characters while preserving apostrophes
  (incl. typographic), unicode letters and emoji. Command replies render as markdown
  only, and raw-HTML notices `html.EscapeString` all names.

#### Residual gaps closed against the sibling bridge

Final parity pass against `jmpchat-matrix-bridge` 0.13.0; where our models differ we
kept ours deliberately:

- **Invite safety** — `connector/xmpp_muc_invites.go` never creates a portal on a
  Cheogram contact JID, never claims non-room JIDs (`isMUCRoom` disco probe with
  heuristic fallback and a 10-minute negative cache), never re-pends an already-joined
  room, and only re-invites when the user is no longer a member.
- **Native Matrix invites for MUC invites** — portals are created with `invite`
  membership (`PortalMetadata.MUCInvitePending`/`MUCLeft`); accepting joins with the
  remembered password and writes the bookmark, rejecting forgets it; rate limit 10
  invites/hour with management notices.
- **Stable IDs + cross-path dedupe** — `connector/xmpp_msgid.go` (WS1.7).
- **Outbound mention names** — `connector/matrix_mentions.go` (WS1.6).
- **MUC retraction echo wait** — `connector/xmpp_muc_echo.go` (WS1.3).
- **Bookmark merge pipeline** — `connector/xmpp_bookmarks_merge.go` (WS2.5).
- **Anonymous-room occupant identity + avatars** — `senderUserID` gives anonymous-room
  occupants their own room-scoped ghost; avatars are probed over XEP-0084 through the
  occupant full JID (`connector/xmpp_occupant_avatar.go`): 4/s pacing, ≤1 MiB, mime
  allowlist, `xep0084:<id>` ids, hash/permanent-error stamping.
- **Build tags** — the Dockerfile builds with pure-Go `goolm` by default
  (`goolm sipcalls nolibopusfile`), dropping libolm from builder and runtime images;
  existing libolm pickle v4 crypto stores keep working.
- **Auto-rejoin hardening** — each staggered rejoin re-checks the left flag mid-run.

#### WS2 - MUC robustness

- **WS2.1 Join health watchdog** — done phase 1 (`connector/xmpp_muc_health.go`): joins
  confirmed via self-presence, retry backoff 30 s -> 10 min, management notice after 5
  failed attempts, XEP-0410 self-ping every 15 min with auto-rejoin on failure.
- **WS2.2 Nick management** — done phase 1+2 (`connector/xmpp_muc_nick.go` +
  `muc-nick`/`muc-nick-default` commands): precedence explicit override (persisted in
  `LoginMetadata.MUCNickOverrides`) > bookmark nick > account default
  (`LoginMetadata.DefaultMUCNick`, else JID localpart); 409 conflicts retry
  `base_1.._3`, remember the working alternative.
- **WS2.3 Nick sync** — done phase 1 (`connector/xmpp_room_meta.go`,
  `connector/xmpp_muc_nick_sync.go`): room renames persist and (with `muc_nick_sync`,
  default true) apply as occupant nick; Matrix topics forward as MUC subjects; member
  displayname changes debounce 3 s and apply as MUC nick unless equal to the global
  profile name; XMPP nick changes write the user's member displayname through double
  puppeting with the `fi.mau.double_puppet_source` marker stripped. Tests in
  `connector/xmpp_muc_nick_sync_test.go`.
- **WS2.4 Auto-rejoin on connect** — done phase 1: `scheduleMUCAutoRejoin` runs after
  both connect hooks, skips left/already-joined rooms, caps at 200 rooms per connect
  with 3 s delay + 500 ms stagger, re-checking the left flag mid-run.
- **WS2.5 Bookmarks XEP-0402 pipeline** — done phase 1+2+3: live PEP events
  (`connector/xmpp_bookmarks_pep.go`), leaving a bookmarked room rewrites the bookmark
  with `autojoin=false`, `bookmarks-sync [push]` republishes known bookmarks, and every
  write is a read-modify-write merge that preserves existing name/password and never
  overwrites a non-empty nick (`connector/xmpp_bookmarks_merge.go`); PEP capability
  gating via disco; one-time XEP-0402 backfill for already-bridged rooms. Tests in
  `connector/xmpp_bookmarks_test.go`, `connector/xmpp_bookmarks_merge_test.go`.
- **WS2.6 MUC invites** — done phase 1+2: direct (XEP-0249) and mediated (XEP-0045)
  invites create the portal as a native Matrix invite; accepting joins with the
  remembered password, rejecting forgets it; rolling rate limit 10 invites/hour with
  management notices. Tests in `connector/xmpp_muc_invites_test.go`.
- **WS2.7 Room topic/subject** — done phase 1 (`connector/xmpp_muc.go`): subject-only
  groupchat stanzas update the Matrix room topic via `ChatInfoChange`
  (`extractMUCSubject`/`handleIncomingMUCSubject`).
- **WS2.8 Group text model validation (design divergence, documented)** — Cheogram
  Android never joins a MUC for group texts (comma JID treated as a contact, messages
  sent as `type=chat`, `MODE_SINGLE`), and the sibling bridge uses the same DM-portal
  model. We keep our MUC-style model (join presence + `type=groupchat` + best-effort
  `muc#owner` config/invites) because it works against the deployed gateway, gives
  occupant/member tracking, and the first message is the real gateway trigger under
  either model. Inbound attribution parity: `resolveMUCSenderID` falls back to the
  stanza resource for Cheogram group rooms and converts it into a phone JID instead of
  collapsing to the room JID (test `TestResolveMUCSenderIDCheogramGroupResource`).

#### WS3 - Identity and contacts

- **WS3.1 vcard-temp avatars (1:1)** — done phase 1+2 (`connector/xmpp_avatar.go` +
  tests): XEP-0153 photo-hash trigger, XEP-0054 vCard fetch (≤1 MiB, png/jpeg/gif/webp),
  upload + re-upload dedupe via `bridgev2.Avatar`/`ghost.UpdateInfo`, stable
  `AvatarID` (`vcard:<hash>`), last-hash caching, 24 h periodic refresh for stale
  roster avatars, and permanent stanza-error stamping so failing photo hashes are not
  retried until they change. Cheogram phone numbers are skipped.
- **WS3.2 MUC occupant avatars** — done phase 1: occupants with a real bare JID
  (non-anonymous rooms) reuse the 1:1 flow; anonymous rooms get room-scoped ghosts and
  XEP-0084 probing through the occupant full JID (`connector/xmpp_occupant_avatar.go`),
  4/s pacing, ≤1 MiB, mime allowlist, `xep0084:<id>` ids.
- **WS3.3 Avatar diagnostic** — done phase 1+2: `avatar-probe` command
  (`connector/xmpp_avatar_probe.go`) reports vcard-temp, XEP-0084 metadata/data and
  disco#info avatar features with timings.
- **WS3.4 Roster management commands** — done phase 1 (`connector/xmpp_roster_manage.go`
  + `connector/commands.go`): `add-to-roster` (alias `save-contact`) with optional
  display name, `remove-from-roster`, `search-roster <query>` (case-insensitive over
  JIDs and resolved names), `sync-roster`.
- **WS3.5 CardDAV import** — done phase 1 (`connector/carddav.go`,
  `connector/carddav_commands.go` + tests): `carddav-login`/`-logout`/`-status`/
  `-sync [apply]`; vCard parsing (FN, TEL incl. Apple `item1.TEL`, folded lines),
  E.164 normalization, roster adds (`setRosterItemCore`, 500/run, 200 ms pacing);
  SSRF guard incl. CGNAT; credentials in login metadata only. Multi-addressbook,
  tombstones, SRV discovery and bulk mode are tracked in `TODO.md`.
- **WS3.5a Contact names, CardDAV takeover, and naming workflow** — done phase 1+2:
  one resolver (`resolveGhostDisplayName`, `connector/naming.go`) with strict source
  precedence explicit `set-contact-name` > CardDAV (`GhostMetadata.CardDAVName`) >
  roster (`RemoteName`) > ghost name > JID label; `carddav-sync` applies names by
  default with a first-apply takeover notice (`--no-apply` previews, `--force-names`
  re-runs naming even for hand-customized rooms); `carddav-clear` and
  `clear-contact-names [--source ...]` clear sources and re-run auto naming;
  `carddav-status` shows per-source counts. Event-driven naming runs on inbound
  messages / new portals / group texts; explicit names are never silently overwritten.
- **WS3.6 Roster isolation** — audited phase 1: every roster-derived refresh path is
  login-scoped and all roster/cache state lives on the per-login `MyNetworkClient`.
  Caveat: bridgev2 ghosts are network-global, so when multiple logins see the same
  contact the most recent roster/presence name write wins until per-login ghost IDs
  are designed (breaking change, needs migration).
- **WS3.7 Presence/last-active** — deferred by design (privacy first): XMPP presence is
  consumed internally but never bridged to Matrix, and no Matrix presence is published.

#### WS4 - Operations and reliability

- **WS4.1 OpenTelemetry** — deferred: requires new dependencies the bridge does not
  vendor. Design stays env-only activation (`OTEL_TRACES_EXPORTER`,
  `OTEL_EXPORTER_OTLP[_TRACES]_ENDPOINT`), protocol http/protobuf default; per-command
  spans, no argument capture.
- **WS4.2 Multi-account UX** — done phase 1: `selectCommandLogin`
  (`connector/command_login.go`) lets chat commands take an optional leading login
  JID/name. Tests in `connector/command_login_test.go`.
- **WS4.3 Connection hardening** — SRV lookup (`_xmpp-client._tcp`) and candidate
  fallback live in the patched-xmpp fork; `_xmpps-client`/XEP-0368 direct TLS, SRV
  preference ordering, and per-login server override are also supported now
  (`TransportConfiguration.DirectTLS`, `resolveSRVCandidates`; per-login override via
  `network.default_server`/`default_port`). Remaining: the
  `network.allow_private_server_override` SSRF guard (incl. CGNAT).
- **WS4.4 Keepalive and connection health** — done phase 1+2: XEP-0199 runtime health
  ping (only when the stream was idle for 90 s, 2 consecutive failures force
  reconnect), 3-minute self-ping to our own full JID, reconnect backoff 5 s doubling to
  5 min, permanent `bad_credentials` on auth failures. Tests in
  `connector/xmpp_health_test.go`.
- **WS4.5 XEP-0198 stream management** — done phase 1+2: resume negotiated and used,
  outbound sends tracked with ack timeouts, message stanzas stored for retransmit, and
  unacknowledged messages re-sent after a fresh (non-resumed) connect with a
  management-room notice.
- **WS4.6 MAM catch-up and backfill** — done phase 1+2: automatic DM and MUC catch-up
  after every connect (`connector/history_sync.go`), archive-wide DM backfill paged
  from the login-level `LastSyncAt` overlap (numbers that texted while offline still
  produce portals), `history_days` lookback (default 30 days) honoured by `sync` and
  `join-room`. Remaining optional polish: an RSM `after`-cursor variant.
- **WS4.7 Config surface and polish** — done phase 1+2: signal toggles `send_typing`,
  `send_read_markers`, `send_delivery_receipts` (*bool, default enabled) gate outbound
  chat states and receipts; `default_server`, `default_port`, `resource_prefix` wired
  into the connection config. E2BE builds use pure-Go `goolm` by default.
- **WS4.8 History retention (TTL)** — done phase 1, in config-schema and command parity
  with the `mautrix-android-sms-mms` bridge: `history_ttl.*` (enabled, global,
  per_room, per_contact, patterns presets `otp`/`credit-card`/`ssn`, `custom_patterns`,
  `delete_media`, `dry_run`, `interval`, `scan_window`, `scan_limit`); a loop walks
  every portal oldest-first and redacts real MXIDs, deletes bridge rows, retries
  failures next run; admin commands `ttl status` / `ttl run`. Files: `connector/ttl.go`,
  `connector/ttlconfig.go`, `docs/history-ttl.md` + tests. Bridge-specific caveat:
  MAM catch-up can re-import a swept message while it is inside its backfill window;
  the next retention run removes it again.

#### WS5 - Calls

- **WS5.1 SIP/MatrixRTC integration** — done phase 1 (opt-in by design): embedded
  `mrtc-sip-bridge` behind the `sipcalls` build tag (`connector/sipcalls*.go`):
  per-login SIP account lifecycle (`sip-login`/`sip-logout`/`sip-status`), MatrixRTC
  wiring (`core.RegisterMatrixRTCHandlers`, `hostkit.NewCallStack`), a `core.CallHost`
  adapter, and XEP-0353 JMI notice suppression while the SIP account is registered.
  Requires JMP self-service SIP credentials for the same number, a LiveKit SFU on the
  homeserver, double puppeting, and unencrypted rooms (call rooms cover encrypted
  portals).
- **WS5.2 JMI notices** — done: XEP-0353 propose/retract/accept/reject/finish (plus
  ringing/proceed) bridge as deterministic `m.notice` call-state messages
  (`connector/xmpp_call.go`), works without the calls build tag.
- **WS5.3 Dialpad surface** — done phase 1+2: `call`/`hangup`/`dtmf`, call rooms
  (`call-room <number>` / `--remove`; `calls.auto_create_call_room`), the Dialpad
  widget (`calls.widget`/`pwa.*`, `connector/sipcalls_widget.go`), roster-only calls
  (`roster-only-calls`, rejection gate on invite), and call history
  (`connector/call_history.go`, `call-history [N]`). Tests: `connector/call_history_test.go`,
  `connector/sipcalls_test.go`.
- **WS5.4 Ports/docs** — phase 1 done: README documents the `sipcalls` build tag,
  config keys and requirements.

### P1 - phone-network polish

- **#7 Typing and chat states** — done (WS1.1).
- **#8 Location and rich URI handling** — done phase 1: inbound `geo:` bodies become
  `m.location` events, outbound `m.location` sends the GeoURI as the message body
  (`connector/xmpp_extras.go`).
- **#9 Stickers, inline images, and BOB/custom-emoji media** — done phase 1: inbound
  XEP-0231 BOB payloads upload as Matrix media (`connector/xmpp_bob.go`, 5 MiB cap) and
  body-only `cid:` references degrade to a filename placeholder. No dedicated sticker
  mapping: sticker-like images ride the normal media path.
- **#10 Message edits, retracts, and moderation tombstones** — done (WS1.2/WS1.3).
- **#11 Invite handling and stranger-invite policy** — done phase 1 (WS2.6).
- **#12 Service outage status** — done phase 1: XMPP headline messages with status text
  (e.g. gateway outage notices) route to the management room as service-status notices
  (`connector/xmpp_extras.go`). Deeper account-level state detection is in `TODO.md`.

### P2 - secondary for this architecture

- **#13 Avatars and richer profile sync** — done (WS3.1/WS3.2); group/portal avatars
  remain in `TODO.md`.
- **#14 Presence and last-active style signals** — deferred by design (WS3.7).
- **#15 Room topics/subjects and room metadata** — done phase 1 (WS2.7); disco#info
  room metadata and topic clearing remain in `TODO.md`.
- **#16 Bookmarks and room persistence semantics** — done phase 1+2 (WS2.5); optional
  XEP-0049 legacy import remains in `TODO.md`.

### P3 - out of scope unless the gateway path proves it matters

- **#17 webxdc handling** — deferred: unknown payloads ride the normal body/media path
  and degrade to text or a notice.
- **#18 Calls and Jingle** — delivered through WS5 (SIP/MatrixRTC); raw Jingle stays
  out of scope.
- **#19 Advanced file transfer modes** — satisfied phase 1: XEP-0363 upload + XEP-0066
  OOB download cover the gateway paths with readable link fallbacks.
- **#20 Registration, onboarding, account management** — out of scope by design: JMP
  account creation stays on the JMP website; gateway-side flows are reachable through
  `gateway-actions`/`gateway-form`/`gateway-submit`.
- **#21 OMEMO (XEP-0384)** — deferred: no OMEMO decryption/encryption; encrypted
  payloads degrade to a notice with ids preserved (WS1.8).

### Validation harness and live acceptance

An env-driven, community-friendly harness proves the bridge end to end:
`scripts/validate/lib.sh` (env contract; evidence is ids, hashes, counts and
timestamps only — never message bodies) with the runbook in `docs/validation.md` and
the smoke/tranche scripts `scripts/validate/t01_*.sh` … `t12_*.sh` plus `fixtures/`.

Against the test deployment the 2026-10-27 tranche proved: outbound SMS and group
creation (exactly one room, no fracture, no own-number ghost), byte-exact attachments
in both directions (SHA-256-verified on the handset), reactions and their handset
fallback, replies with the `> ` quote fallback, edits/redactions (the bridge posts a
"cannot be unsent" notice and the handset copy stays), a receipts capability probe,
one group event reaching both handsets, idempotent new-DM creation, and restart dedupe
(no re-inserted messages, row counts unchanged). The full checklist and the still-open
items (second paired device, failure drills, newer invite/bookmark round trips) live
in `TODO.md`.

Gotchas worth remembering: the bridge app build can only read files inside its own
app directory (no media permission); `control.SEND` refuses files owned by other apps;
bridge notices arrive as `m.room.message` with `msgtype: m.notice`; and
`systemctl restart` on the bridge unit can lose a container-name race (retry and
confirm with `docker inspect`).

## Design considerations

- **Gateway-first priorities.** Highest priority is not general XMPP client parity but
  the semantics that survive the phone-network gateway: gateway workflows,
  phone-number identity, MMS-style media fidelity, receipts/status. Native-XMPP product
  polish is deprioritized unless the gateway exposes it usefully.
- **Group text model** — see WS2.8: MUC-style model kept deliberately; switching to
  chat-type requires live gateway validation first.
- **Multi-login ghost naming** — see WS3.6: ghosts are network-global; per-login ghost
  IDs would be a breaking change and need a migration design.
- **Privacy defaults** — presence is never bridged (WS3.7); CardDAV credentials live in
  login metadata only; the bridge never logs message bodies or credentials.
- **ID and dedupe philosophy** — prefer stable, scope-qualified ids (WS1.7); never drop
  contact messages on sender-chosen ids; dedupe only replays we can attribute.
- **Connection health** — idle-gated health pings plus a full-JID self-ping (WS4.4),
  stream resumption with re-send after fresh connects (WS4.5), and bounded reconnect
  backoff with permanent `bad_credentials` state.
- **History retention** — TTL parity with the sms-mms bridge (WS4.8), with the MAM
  re-import caveat documented above.
- **Calls** — opt-in (`network.calls.enabled: false` disables the SIP runtime),
  calling ships in the standard image behind the `sipcalls` build tag, and encrypted
  portals use linked call rooms until SFrame/E2EE lands
  (`docs/SFRAME_CALLS_SPIKE.md`).
- **Build tags** — `goolm` pure-Go crypto by default; `sipcalls`/`nolibopusfile` for
  calling builds.

## Operational lessons

- **Portal rooms vs direct rooms.** There are two ways to create a room in the mautrix
  bridge architecture: direct Matrix rooms (`c.bridge.Bot.CreateRoom`) exist only in
  Matrix and never trigger `HandleMatrixMessage`; portal rooms
  (`portal.CreateMatrixRoom`) are registered with the bridge, mapped to a remote
  chat, and are the only rooms whose events reach the network handlers. If
  `HandleMatrixMessage` is not firing, check that the room is a portal. The
  `AGENTS.md` file carries the same note for contributors.
- **Deployment verification before debugging.** A configuration parse failure on a
  deployed host (for example `cannot unmarshal !!str ... into connector.SimpleConfig`)
  is usually a deployment problem first and a code problem second: verify that the
  host actually runs the freshly built image and that the rendered `network:` block is
  the inline mapping (not a scalar) before changing code. Stale image tags, cached
  images, or a miswired build/pull path are the most common causes.

## Current state

The bridge is functionally complete for the gateway path: messaging parity, MUC
robustness, identity/contacts, reliability, and the opt-in calling stack are in place
and covered by unit tests. What remains is mostly live validation on the test
deployment plus a small set of optional polish items — see `TODO.md`.
