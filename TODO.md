# TODO

Bridge parity backlog derived from a deep scan of `cheogram-android/` against the current `mautrix-jmpchat` connector, merged with an exhaustive study of `jmpchat-matrix-bridge/` (williamkray, formerly `mautrix-jabber`).

The single-bridge backlog below is kept as-is; the adopted material from the other bridge lives in the **Workstreams** section.
Cross-references: `WSx` = adopted workstream item, `#n` = original numbered backlog item.

## Architecture note

This backlog is for a bridge path closer to:

`Matrix <-> XMPP/Cheogram/Snikket <-> cheogram/jmp.chat gateway bridge <-> SMS/MMS <-> phones (and sometimes iMessage/RCS by proxy)`

That changes priorities.

- Highest priority is not general XMPP client parity; it is preserving the semantics that survive the phone-network gateway.
- Features that matter most are gateway workflows, phone-number identity, MMS-style media fidelity, receipts/status, and anything likely to round-trip through SMS/MMS or gateway-mediated iMessage/RCS.
- Features that are mostly native-XMPP product polish should be deprioritized unless the gateway actually exposes them in a useful way.
- Adopted workstreams from `jmpchat-matrix-bridge` bring production-hardened behavior for messaging semantics, MUC handling, identity/contacts, operations, and calls; port them selectively, not wholesale.

## Current baseline

Already covered well enough to avoid duplicating here:

- basic XMPP login and login validation
- DM and group portal creation/sync
- MAM backfill and bookmark-driven room discovery
- text messages in both directions
- basic media/file bridging via HTTP upload/OOB/SIMS
- basic reactions in both directions
- native bridgev2 provisioning: group creation (`CreateGroup` / `GroupCreatingNetworkAPI`, `create-group` command, `POST /v3/create_group/{type}`), identifier resolution (`ResolveIdentifier`, `CreateChatWithGhost`), contact list (`GetContactList`), `ValidateUserID`
- management-room group/room tooling: `create`, `join-room`, prune/rebind/replace room and space commands, `sync-status`

Major visible gaps still cluster around modern messaging polish, gateway workflows, and richer XMPP account features.

## P0 - gateway-critical parity work

These are the items most likely to materially improve Matrix <-> phone-network conversations.

### 1. Gateway-specific UX for PSTN/SMS flows

Why:
- This is central to JMP/Cheogram identity and matters more than generic XMPP feature parity.
- Phone-number creation, gateway prompts, and contact-add flows are core to the real user journey here.

Backlog:
- improve phone-number creation/join flows to match gateway expectations
- support `jabber:iq:gateway` discovery and prompts
- expose contact-add or registration flows for SMS/PSTN gateways
- make gateway/service actions easy to trigger from Matrix management rooms

Evidence:
- `cheogram-android/README.md`
- `cheogram-android/src/main/java/eu/siacs/conversations/ui/EnterJidDialog.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/ui/ConversationsActivity.java`
- `connector/commands.go`

Status: done phase 1 — phone-number creation/join flows work through the management-room `create`/`start-chat` commands and the native bridgev2 provisioning endpoints (`create-group`, `resolve-identifier`); `jabber:iq:gateway` discovery and prompts are covered by `gateway-actions` (`connector/xmpp_gateway.go`: `discoverGatewayActions`, `requestGatewayPrompt`, `SupportsGatewayIQ`); contact-add flows exist as `add-to-roster`/`save-contact` plus the CardDAV import, and gateway/service registration flows are reachable through `gateway-actions`, `gateway-form` and `gateway-submit`. Remaining phase 2: interactive gateway-prompt resolution when an identifier is not already a JID (auto-query `jabber:iq:gateway` for foreign gateways), richer form UX, and surfacing gateway prompts for unknown contacts as guided notices.

### 2. Rich media messages with captions, subjects, and cleaner metadata

Why:
- MMS and voicemail-style traffic lives or dies on attachment fidelity.
- Cheogram explicitly supports media+text messages and subject lines, which map closely to MMS-style behavior.

Backlog:
- preserve captions consistently when media and text are both present
- decide how XMPP `subject` should surface on Matrix for DM-style gateway traffic
- improve per-message file metadata mapping where Cheogram exposes name, size, duration, and thumbnails
- keep graceful fallbacks when remote media download or re-upload fails

Evidence:
- `cheogram-android/README.md`
- `cheogram-android/src/main/java/eu/siacs/conversations/parser/MessageParser.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/ui/adapter/MessageAdapter.java`

- `connector/network_client.go`

Status: done phase 1 — captions are extracted from XEP-0066/XEP-0454 attachments and merged with subject lines (`mergeXMPPSubjectAndBody`); downloaded and BOB media always get `m.file`/`m.image`/`m.audio`/`m.video` metadata (mime, size, width/height, duration) and voice notes are recognized (MSC1767/MSC3245); failed downloads keep a readable link fallback. Remaining phase 2: thumbnail mapping and per-message name parity for niche Cheogram extensions.

### 3. Read receipts, chat markers, and delivery/displayed state

Why:
- Delivery/read status is one of the few pieces of modern messaging polish that users notice immediately in SMS/RCS/iMessage-like flows.
- Cheogram already models this, and the bridge currently does not.

Backlog:
- send Matrix read markers to XMPP chat markers / receipts
- parse inbound receipts and displayed markers into Matrix read state where possible
- distinguish delivery vs displayed semantics if the gateway exposes both

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/generator/MessageGenerator.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/parser/MessageParser.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/manager/MessageDisplayedSynchronizationManager.java`
- `connector/xmpp_message_extensions.go`

### 4. Command UI and ad-hoc form workflows

Why:
- For this architecture, command/forms matter less as a general XMPP feature and more as the control plane for gateway registration and account actions.
- They likely unlock real JMP/SMS tasks that plain chat messages cannot.

Backlog:
- discover ad-hoc commands on contacts/services
- represent XMPP forms in Matrix in a usable way
- submit filled forms back over XMPP
- prioritize gateway registration/account-management commands first

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/entities/Conversation.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/ui/ConversationFragment.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/services/XmppConnectionService.java`
- `cheogram-android/README.md`

### 5. Replies and fallback quoting

Why:
- Replies are useful if the gateway preserves them for RCS/iMessage-like flows or renders reasonable quote text for SMS/MMS.
- Still important, but not as universally critical as gateway flows, attachments, and receipts.

Backlog:
- map Matrix replies to XMPP `urn:xmpp:reply:0`
- parse inbound XMPP replies and attach Matrix reply relations
- preserve `urn:xmpp:fallback:0` handling so fallback text is not double-rendered

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/entities/Message.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/ui/adapter/MessageAdapter.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/services/XmppConnectionService.java`
- `connector/handle_matrix.go`

### 6. Finish reactions parity

Why:
- Reactions matter more if the downstream side is really proxying iMessage/RCS semantics, and less if the traffic is plain SMS/MMS.
- The bridge already has a partial implementation, so closing the gap may be relatively cheap.

Backlog:
- support Matrix reaction removal -> XMPP removal or best-effort replacement behavior
- verify MUC and private-message reaction target resolution
- evaluate custom emoji / image reactions and degrade them predictably on Matrix

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/entities/Reaction.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/parser/MessageParser.java`
- `cheogram-android/CHANGELOG.md`
- `connector/handle_matrix.go`

Status: done — reaction removal is expressed as a full-set replacement for that sender (`currentMatrixReactionSet` + `sendXMPPReactionSet`, `HandleMatrixReactionRemove`); targets resolve by bridgev2 id first and then via persisted XMPP alias metadata (`resolveReactionTarget`, WS1.5), covering MUC and private-message reactions; image-based custom-emoji reactions (`mxc://`) are stored in Matrix but deliberately not sent to XMPP (`xmppReactionSupported`/`filterXMPPReactions`, tests `connector/xmpp_reaction_filter_test.go`), and an all-unsupported set is never sent so remote reactions are not wiped.

## Workstreams adopted from jmpchat-matrix-bridge

Reference implementation: `jmpchat-matrix-bridge/pkg/connector/` (file names below are entry points in that repo).
Each item must keep our SPDX headers, test style, and terse management-room UX.

### WS1 - Messaging parity

- **WS1.1 Typing and chat states** (also `#7`) — done in `connector/xmpp_typing.go` (outbound `HandleMatrixTyping` -> XEP-0085 `composing`/`paused`; inbound state parse -> `simplevent.Typing`; all Cheogram targets suppressed; hook in `network_client.go` before delay/attachment handling). Tests in `connector/xmpp_typing_test.go`. Toggle: `send_typing` (wired in WS4.7).
  - Reference: `xmpp_signals.go`, `xmpp_signals_out.go` (their suppression rules `:35-54`, `:135-141`).
- **WS1.2 Corrections (XEP-0308) both ways** (also `#10`) — done in `connector/xmpp_corrections.go` (outbound Matrix edit -> `<replace id=...>`; Cheogram/SMS edits sent as new message prefixed `* `; inbound resolution via direct id + persisted `origin_id`/`xmpp_message_id`/`stanza_id` aliases; sender ownership check; media and unknown/foreign targets ignored). Tests in `connector/xmpp_corrections_test.go`. Phase-1 limitations: no KV replay shield (correction replay is idempotent), correction-of-correction resolves to the same Matrix event but is not re-mapped to the original stanza id.
  - Reference: `xmpp_correct.go`, `xmpp_correct_out.go`.
- **WS1.3 Retractions (XEP-0424/0425) both ways** — done in `connector/xmpp_corrections.go` (outbound Matrix redaction -> XEP-0424 `<retract>`; Cheogram/SMS targets get a "cannot unsend" notice instead of a stanza; inbound XEP-0424/0425 -> Matrix redaction via the same alias resolution, moderation matches by room stanza-id only and skips the sender check; body-less retraction stanzas bypass the empty-body guard). Tests in `connector/xmpp_corrections_test.go`. Outbound MUC retractions now wait up to 3 s for the room-assigned stanza-id learned from the echo of our own message (`connector/xmpp_muc_echo.go`: `recordMucEcho`/`waitMucEcho`, 5 min TTL, origin-id fallback). Phase-1 limitations: no KV tombstone against MAM replay resurrection, no MUC-moderation authority check.
  - Reference: `xmpp_retract*.go`, `xmpp_muc_echo.go`.
- **WS1.4 Multiple reactions per user per message** — done (verified, no new code needed): outbound already sends the full active set via `currentMatrixReactionSet` + `sendXMPPReactionSet` (`connector/handle_matrix.go`); inbound reactions queue a per-sender `simplevent.ReactionSync` with `HasAllReactions=true`, so multiple emojis per user no longer collapse. Cross-check remaining removal polish with P0 `#6`.
  - Reference: their README To-do #2 and ROADMAP "Multiple reactions".
- **WS1.5 Reaction target resolution hardening** — done: every converted message part persists `XMPPMessageID`/`OriginID`/`StanzaID` (inbound `network_client.go` DBMetadata, outbound `handle_matrix.go` response), and `nc.resolveReactionTarget` (`connector/xmpp_corrections.go`) resolves XEP-0444 targets by direct bridgev2 id first, then via alias lookup (`resolveXMPPRefTarget`, SQL `metadata->>'...'`) scoped to the portal. Reactions to XMPP-originated MUC messages now find their target; the old 30-day scan is replaced.
  - Reference: `xmpp_ids.go`, their README To-do #1.
- **WS1.6 At-mentions both ways** — done (phase 1, XEP-0372 references): outbound Matrix `m.mentions` -> `<reference type="mention" uri="xmpp:<jid>"/>` (`nc.xmppMentionReferences` -> `ParseGhostMXID`, `connector/xmpp_mentions.go`, wired in `HandleMatrixMessage`); inbound XEP-0372 mention references -> Matrix `m.mentions` pills (`nc.xmppMentionMXIDs` -> `FormatGhostMXID`, ensuring ghosts exist), wired in `ConvertMessageFunc`. Tests in `connector/xmpp_mentions_test.go`. Outbound plain-body mention names are rewritten to the XMPP-side name for bridged users (`rewriteMentions`/`xmppNameForGhost`, `connector/matrix_mentions.go`, wired into messages and edits), HTML-only events get a mention-aware plain body. Remaining (future): nick-heuristic parsing of plain-text mentions (`nick:` / whole-word), XEP-0372 refs on outbound MUC messages could carry the nick instead of the bare JID.
  - Reference: `xmpp_mentions_in.go`, `matrix_mentions.go`.
- **WS1.7 Message IDs, dedupe, and timestamps** — done (phase 1+2): ID tiers are `id` attribute -> scope-qualified stanza-id (`xmp:sid:<scope>:<id>`, only when `by` matches the sender bare JID or its domain, own bare JID for carbons/isFromMe) -> `origin-id` -> delay-hash (SHA-256 over scope/sender/trusted delay/body/OOB, used for history replays) -> random `xmp:r:<uuid>` (`connector/xmpp_msgid.go`, tests in `connector/xmpp_msgid_test.go`). Trusted XEP-0203 delays are accepted only from own server domain, the sender's domain, or the room; cross-path dedupe (30-day window) drops stanza-id replays of anyone's messages and origin-id/alias replays of our own — contact messages are never dropped on sender-chosen ids (`isBridgedDuplicate`, `aliasMessageIDsSince`, `connector/xmpp_corrections.go`). Remaining (future): nothing outstanding; RSM-cursor MAM polish lives under WS4.6.
  - Reference: `xmpp_msgid.go`, their README "Duplicates and message times".
- **WS1.8 Encrypted payload degradation** — done in `connector/xmpp_encrypted.go` + tests in `connector/xmpp_encrypted_test.go`: OMEMO/OpenPGP/XEP-0380 EME/`aesgcm://` detected -> fixed `m.notice` ("🔒 Encrypted XMPP message (<kind>) — this bridge cannot decrypt it."), never downloaded, ids preserved so replies/corrections/retractions still map. Body-less encrypted key-transport also surfaces as the notice instead of being dropped. Remaining: OMEMO decryption itself (see `#21`).
  - Reference: `xmpp_subject_encrypted.go`, `xmpp_extras_parse.go`.

### Room naming fixes (deployed user bugs, done)

- **Roster name import + refresh** — done in `connector/xmpp_roster.go`: XMPP roster `item.Name` (previously unused) now stored into `GhostMetadata.RemoteName` (explicit `CustomName` always wins) during roster sync; when any name changed, `refreshAutoNamedPortals` reruns so auto-named DM/group rooms pick the name up. Fixes DM room titles stuck on the bare phone number.
- **Initials self-heal** — done in `connector/naming.go`: a participant with an empty label now breaks the "all named" initials path instead of being silently dropped (`SM` for M+S+R), and `portalLooksAutoNamed` treats any all-uppercase initials-like name with 1..N runes (`initialsLikeName`) as auto-named, so short/stale initials (`SM (jmp)` in a 3-person room) regenerate to `SMR`. Tests: `connector/naming_test.go` (`TestInitialsLikeName`, `TestPortalLooksAutoNamed`).
- **Special characters in contact names** — audited + hardened: `normalizeContactName` (`connector/naming.go`) trims, collapses whitespace runs to one space and drops control characters while preserving apostrophes (`O'Brien`), typographic apostrophes/quotes, unicode letters and emoji (incl. ZWJ sequences); applied by `set-contact-name` (`connector/commands.go`) and roster import (`connector/xmpp_roster.go`). Safety audit: command replies render as markdown only (no HTML), and the two raw-HTML room notices (`existingRoomNotice`/`createdRoomNotice`) `html.EscapeString` all names. Tests: `TestNormalizeContactName`, `TestContactInitialSpecialChars`, `TestGroupInitialsSpecialChars`, `TestGroupRoomNameExplicitSpecialChars`, `TestSuffixSpecialChars`, `TestPortalLooksAutoNamedApostropheName` (naming_test.go), `TestSpecialCharactersInJIDs` (jid_test.go), `TestRoomNoticesEscapeNames` (commands_test.go), `TestRosterItemNameDecoding` (xmpp_roster_test.go), metadata JSON round-trips in `types_test.go`.

### Residual gaps vs the sibling bridge (done)

Final parity pass against `jmpchat-matrix-bridge` 0.13.0; reference files are that repo's `pkg/connector/`. Where our models differ we kept ours deliberately.

- **Invite safety** — `connector/xmpp_muc_invites.go` never creates a portal on the key of a Cheogram contact JID (a direct invite naming a number can no longer divert later DMs), never claims non-room JIDs (`isMUCRoom` disco probe with `@conference.`/`@muc.`/`@groups.` heuristic fallback and a 10-minute negative cache), never re-pends a room that is already joined, and only re-invites when the Matrix user is no longer a member.
- **Native Matrix invites for MUC invites** — MUC invites are surfaced as native Matrix invites instead of auto-joins (portal created with `invite` membership, `PortalMetadata.MUCInvitePending`/`MUCLeft`). Accepting (`HandleMatrixMembership`) joins the MUC with the remembered invite password and writes the bookmark; rejecting forgets the password; the portal-room notice for an already-joined room points at `join-room`. Rate limit stays 10 invites/hour with management notices.
- **Stable IDs + cross-path dedupe** — `connector/xmpp_msgid.go` (see WS1.7).
- **Outbound mention names** — `connector/matrix_mentions.go` (see WS1.6).
- **MUC retraction echo wait** — `connector/xmpp_muc_echo.go` (see WS1.3).
- **Bookmark phase 3** — `connector/xmpp_bookmarks_merge.go` (see WS2.5).
- **Anonymous-room occupant identity + avatars** — new `senderUserID` in `connector/network_client.go` gives anonymous-room occupants their own room-scoped ghost (`room@conference.example.com/nick`), matching the reference's occupant ghosts (slashes are Matrix-localpart-safe and `contactLabelFromJID` already resolves the nick as the display name). Avatars for them are probed over XEP-0084 (`urn:xmpp:avatar:metadata`/`data` through the occupant full JID, `connector/xmpp_occupant_avatar.go`): 4/s pacing, ≤1 MiB, `http.DetectContentType` + png/jpeg/gif/webp allowlist, `xep0084:<id>` avatar IDs, last-hash/permanent-error stamping in `GhostMetadata`. Tests: `connector/xmpp_occupant_avatar_test.go`.
- **Build tags** — the Dockerfile now builds with pure-Go `goolm` by default (`goolm sipcalls nolibopusfile`), dropping libolm from builder and runtime images; existing libolm pickle v4 crypto stores keep working (README "Build tags (calling support)").
- **Auto-rejoin hardening** — each staggered rejoin re-checks the left flag mid-run (`connector/xmpp_muc_health.go`).

### WS2 - MUC robustness

- **WS2.1 Join health watchdog** — done (phase 1, `connector/xmpp_muc_health.go`): joins confirmed via self-presence, retry backoff 30 s -> 10 min, management notice after 5 failed attempts, XEP-0410 self-ping (IQ get to our occupant JID) every 15 min with auto-rejoin on failure. Remaining: verify behaviour against a live room that rejects joins outright.
- **WS2.2 Nick management** — done (phase 1+2, `connector/xmpp_muc_nick.go` + `muc-nick`/`muc-nick-default` commands): precedence explicit `muc-nick` override (persisted in `LoginMetadata.MUCNickOverrides`) > bookmark nick > account default (`LoginMetadata.DefaultMUCNick` from `muc-nick-default`, else JID localpart); on 409 conflicts try `base_1.._3`, remember the working alternative, management notices. Remaining (phase 2): revert room display name on failed explicit change.
- **WS2.3 Nick sync** — done phase 1: `RoomNameHandlingNetworkAPI`/`RoomTopicHandlingNetworkAPI` handled in `connector/xmpp_room_meta.go`; renaming a MUC portal room persists the name (`NameSet`/`NameIsCustom`) and, with `muc_nick_sync` enabled (default true), applies nickname-like names as the occupant nick (override persisted via `setMUCNickOverride`); Matrix topic changes are forwarded as MUC subject changes (XEP-0045) and recorded on the portal. Member-displayname sync (the reference's model) is also implemented in `connector/xmpp_muc_nick_sync.go`: per-room Matrix member displayname changes are debounced 3 s and applied as the MUC nick unless the name equals the global profile name (invalid names post a portal notice); XMPP nick changes (own confirmed renames and foreign renames) write the user's member displayname through double puppeting with the `fi.mau.double_puppet_source` marker stripped and an own-write echo table, plus a one-time marker cleanup per portal after joining. Tests in `connector/xmpp_muc_nick_sync_test.go`. Remaining: live validation of cross-client rename races.
- **WS2.4 Auto-rejoin on connect** — done (phase 1): `scheduleMUCAutoRejoin` in `connector/xmpp_muc_health.go` runs after both connect hooks, skips left/already-joined rooms, caps at 200 rooms per connect with 3 s delay + 500 ms stagger; each staggered join re-checks the left flag mid-run. Remaining: history-since-newest-bridged-message refinement for rejoins.
- **WS2.5 Bookmarks XEP-0402 pipeline** — done (phase 1+2): live PEP events in `connector/xmpp_bookmarks_pep.go` (autojoin on -> join/import via `handleBookmarkConferences(..., "bookmarks2_event")`; off/retract -> `leaveMUC` + bookmark state cleanup); leaving a bookmarked room now rewrites the bookmark with `autojoin=false` (so a reconnect does not silently rejoin) and clears local state; `bookmarks-sync [push]` lists known bookmarks and republishes them with the remembered name/nick (`bookmarkSnapshot`/`pushBookmarks` in `connector/xmpp_bookmarks.go`, tests in `connector/xmpp_bookmarks_test.go`). Remaining (phase 3) done in `connector/xmpp_bookmarks_merge.go` + tests in `connector/xmpp_bookmarks_merge_test.go`: every write is a read-modify-write merge that fetches the current item first, preserves an existing name/password and never overwrites a non-empty nick, and always publishes with `<publish-options>`; PEP capability gating via disco (`bookmarkGate`, fail-open on disco errors, session write-off on conflict/unsupported errors); one-time XEP-0402 backfill for already-bridged rooms (5 s settle, 200 ms pacing, marker `LoginMetadata.BookmarksBackfilled`, management notice; `bookmarks-sync push` forces it). Remaining: optional read-only XEP-0049 legacy import.
- **WS2.6 MUC invites** — done phase 1: direct (XEP-0249) and mediated (XEP-0045) invites still create the portal and join the room, but now go through a rolling rate limit of 10 invites/hour per login (`connector/xmpp_muc_invites.go`, `mucInviteLimiter`); invites beyond the limit are ignored with a warning log and a management-room notice. Successful joins post a "Joined MUC room … after an invite from …" notice. Tests: `connector/xmpp_muc_invites_test.go`. Phase 2 done — invites no longer auto-join: the portal is created with the Matrix user as an invited member (`PortalMetadata.MUCInvitePending`/`MUCLeft`, `connector/xmpp_muc_invites.go`), accepting (`HandleMatrixMembership`) joins the MUC with the remembered invite password and writes the bookmark, rejecting forgets it; non-contact and joined-room invites follow the safety rules above. Password memory persists in `mucInvitePasswords` across rejoins. Remaining: live validation of the accept/reject round trip.
- **WS2.7 Room topic/subject** — done (phase 1, `connector/xmpp_muc.go`): subject-only groupchat stanzas update the Matrix room topic via `ChatInfoChange` (`extractMUCSubject` / `handleIncomingMUCSubject`). Remaining: disco#info `muc#roominfo` name/description on resync and subject-less clears topic.
- **WS2.8 Group text model validation (design divergence)** — documented (phase 1) + attribution parity fix. Evidence gathered: Cheogram Android never joins a MUC for group texts (the comma JID is created as a plain contact, `StartConversationActivity.java:956-969`) and sends chat-type messages because the conversation stays `MODE_SINGLE` (`generator/MessageGenerator.java:53-67`); the sibling bridge uses the same DM-portal model (`type=chat` to the combined JID, no join), and JMP's FAQ lists group texts as beta without protocol detail. We keep our MUC-style model (join presence + `type=groupchat` + best-effort `muc#owner` config/invites) because it works against the deployed gateway, gives occupant/member tracking, and the first message is the real gateway trigger under either model; switching to chat-type needs live gateway validation first. Inbound attribution parity is now explicit: `resolveMUCSenderID` falls back to the stanza resource for Cheogram group rooms and converts it into a phone JID (`+1416…@cheogram.com` or bare digits) instead of collapsing to the room JID, matching their resource → `<xmpp:JID>` body prefix → phone prefix → own-JID ladder (`connector/network_client.go`, test `TestResolveMUCSenderIDCheogramGroupResource`). Remaining: live round-trip validation against the JMP gateway before considering any outbound model change.
  - Reference: `cheogram.go`, `xmpp_inbound.go:889-909`, `xmpp_client.go:2107-2115`.

### WS3 - Identity and contacts

- **WS3.1 vcard-temp avatars (1:1)** — done phase 1 in `connector/xmpp_avatar.go` + tests in `connector/xmpp_avatar_test.go`: XEP-0153 presence photo-hash trigger, XEP-0054 vCard fetch (IQ get, base64 `BINVAL`, ≤1 MiB, png/jpeg/gif/webp only, `http.DetectContentType`), upload + re-upload dedupe via `bridgev2.Avatar`/`ghost.UpdateInfo`, stable `AvatarID` (`vcard:<hash>`) and last-hash cached in `GhostMetadata.AvatarHash` so unchanged avatars are not re-fetched. Cheogram phone numbers are skipped. Also done (phase 2): 24 h periodic refresh when the contact stays online without re-announcing and permanent stanza-error stamping — `startVCardAvatarRefreshLoop`/`refreshStaleVCardAvatars` (500 ms staggered, `GhostMetadata.AvatarFetchedAt`) re-fetch stale roster avatars daily, and permanent stanza errors are stamped (`permanentVCardError`, `GhostMetadata.AvatarFetchErrorHash`) so a failing photo hash is not retried until it changes. Tests: `TestVCardAvatarNeedsRefresh`, `TestIsPermanentVCardError`. Remaining phase 3: group/portal avatars.
  - Reference: `xep0054.go`, `avatar_probe.go`.
- **WS3.2 MUC occupant avatars** — done phase 1: occupants with a real bare JID (non-anonymous rooms) reuse the 1:1 XEP-0153/vcard flow in `connector/xmpp_avatar.go`, hooked from the MUC presence branch in `connector/network_client.go`, so they share the ghost's uploaded avatar. Anonymous rooms are now covered via XEP-0084: occupants get their own room-scoped ghost (`senderUserID`) and the bridge probes `urn:xmpp:avatar:metadata`/`data` through the occupant full JID (`connector/xmpp_occupant_avatar.go`) with 4/s pacing, ≤1 MiB, mime allowlist, `xep0084:<id>` avatar IDs and per-avatar hash/error stamping (the ghost-level dedupe acts as the MXC cache). Remaining: a room-level refusal breaker if a service rate-limits avatar probes.
  - Reference: `xep0054_muc.go`.
- **WS3.3 Avatar diagnostic** — done phase 1+2: `avatar-probe` command in `connector/xmpp_avatar_probe.go` reports vcard-temp (bytes/mime/timing), XEP-0084 metadata (id/bytes/dimensions), XEP-0084 data payload size (base64 decoded, whitespace tolerant via `parseAvatarData`), and disco#info avatar features with timings; tests in `connector/xmpp_avatar_probe_test.go`. Remaining: phase 3 done for MUC occupants (WS3.2 fetches the data node through occupant full JIDs); the probe keeps its diagnostic role for contacts.
  - Reference: `avatar_probe_command.go`.
- **WS3.4 Roster management commands** — done phase 1 in `connector/xmpp_roster_manage.go` + `connector/commands.go`: `add-to-roster` (alias `save-contact`) with optional display name (routes through `normalizeContactName` + `updateRosterContactName` + auto-named room refresh), `remove-from-roster`, `search-roster <query>` (case-insensitive over JIDs and resolved names), `sync-roster` (requests roster refresh). Tests in `connector/xmpp_roster_manage_test.go`. Remaining phase 2: `save-contact` should also write the server vCard, bulk mode.
  - Reference: `roster_contacts.go`, `save_contact.go`, `main.go:220-333`.
- **WS3.5 CardDAV import** — done phase 1 in `connector/carddav.go` + `connector/carddav_commands.go` + tests: commands `carddav-login <url> <user> <pass>`, `-logout`, `-status`, `-sync [apply]` (dry run by default); config `carddav.{allow_insecure_http,allow_private_hosts,default_country_code}`; fetch + parse vCards (FN, TEL incl. Apple `item1.TEL`, folded lines, 32 MiB cap), E.164 normalization (`tel:`, `00`, country code, formatting), add `+E164@cheogram.com` straight to the XMPP roster (`setRosterItemCore`, 500 adds/run, 200ms pacing), names flow into ghosts/room names via roster import; SSRF guard incl. CGNAT; credentials in login metadata only. Remaining phase 2: PROPFIND/REPORT addressbook discovery (phase 1 expects a direct `.vcf` export URL), multi-addressbook, tombstones for deliberately removed entries, CardDAV-name source (`CardDAVName`, per WS3.5a), SRV discovery, `phone.Normalize` parity cases, bulk mode.
  - Follow-ups: DNS SRV discovery, tombstones for deliberately removed roster entries, `phone.Normalize` (fixes `15551234567` -> `+115551234567` and rejected `(555) 123-4567`, honours country code), bulk roster mode.
  - Reference: `carddav_*.go`.
- **WS3.5a Contact names, CardDAV takeover, and naming workflow (design locked)** — one resolver used by ghost info, DM room names and group initials, with strict source precedence: explicit `set-contact-name` > CardDAV (when configured and synced) > XMPP roster name > live ghost name > JID/phone label. Sources are stored separately (`GhostMetadata.CustomName` / new `CardDAVName` / `RemoteName`) so no flow ever overwrites another and clearing one falls back cleanly.
  - `set-contact-name <jid> <name>` stays the explicit override (always wins); `set-contact-name <jid> --clear` drops it so CardDAV/roster take over (implemented).
  - CardDAV lifecycle: `carddav-login` -> first `carddav-sync` posts a takeover notice and applies names to ghosts + auto-named rooms by default (`--no-apply` opt-out); `carddav-clear` removes CardDAV-sourced names and refreshes rooms back to roster/fallback; `carddav-status` shows counts per source.
  - Generalized cleanup: `clear-contact-names [--source explicit|carddav|roster] [<jid>]` (may ship as flags on the existing commands first).
  - Event-driven naming: the same resolver runs on inbound messages / new portals / group texts, so chats are named as they arrive, not only at sync time; every source update triggers the existing auto-named room refresh (`refreshAutoNamedPortals` / `refreshContactNamedPortals`).
  - UX rules: never silently overwrite explicit names; CardDAV renames only auto-named rooms (custom names untouched unless `--force-names`); one summary notice per sync; conflicts logged with both values and the winning source.
  - Migration: first CardDAV apply renames existing auto-named rooms; `--clear`/`carddav-clear` restores the previous precedence chain. Self is never injected into group initials; if the gateway lists your own number in a group JID it is named like any other participant.
  - Status (phase 1+2, done): `GhostMetadata.CardDAVName` stores CardDAV-sourced names separately; `resolveGhostDisplayName` (`connector/naming.go`) enforces the precedence CustomName > CardDAVName > RemoteName > ghost name > JID label; `carddav-sync` imports and writes CardDAV names by default (`setGhostCardDAVName`, `connector/contact_names.go`) with a first-apply takeover notice, `--no-apply` previews instead, and `--force-names` re-runs naming even for hand-customized rooms (`refreshAllPortalNames`); conflicts with explicit names are counted and logged with the winning source; `carddav-clear` and `clear-contact-names [--source explicit|carddav|roster] [<jid>]` clear sources and re-run auto naming; `carddav-status` shows per-source name counts. Tests: `TestParseCardDAVSyncArgs`, `TestResolveGhostDisplayName`, `TestParseContactNameSource`.
  - Reference: `carddav_*.go`, `save_contact.go`, `naming.go`.
- **WS3.6 Roster isolation** — audited phase 1: every roster-derived refresh path is already login-scoped (`refreshRosterNamedPortals`, `refreshAutoNamedPortalsMatching`, `refreshContactNamedPortals` all iterate `DB.UserPortal.GetAllForLogin(login.UserLogin)`), and all roster/cache state lives on the per-login `MyNetworkClient`. Caveat (phase 2): bridgev2 ghosts are network-global (`MakeUserID(jid)` has no receiver), so when the bridge runs multiple logins that see the same contact, the most recent roster/presence name write wins until per-login ghost IDs are designed (breaking change, needs migration).
  - Reference: their ROADMAP 2.1b / To-do #6.
- **WS3.7 Presence/last-active** (also `#14`) — deferred by design for now (privacy first): XMPP presence is consumed internally (MUC joins, avatar hash triggers) but never bridged to Matrix, and we do not publish Matrix presence back. Revisit only if there is a concrete user request and a privacy story.

### WS4 - Operations and reliability

- **WS4.1 OpenTelemetry** — deferred phase 2: requires new OpenTelemetry dependencies the bridge does not currently vendor. Design stays env-only activation (`OTEL_TRACES_EXPORTER`, `OTEL_EXPORTER_OTLP[_TRACES]_ENDPOINT`), protocol http/protobuf default; per-command spans, no argument capture; optional backends (e.g. Datadog) via standard env. Revisit when dependency additions are acceptable.
  - Reference: `pkg/telemetry/`, `main.go:34-56`.
- **WS4.2 Multi-account UX** — phase 1 done: `selectCommandLogin` (`connector/command_login.go`) lets chat commands (`start-chat`/`create`, `join-room`, `muc-nick`) take an optional leading login JID/name matching one of the user's logins, never consuming an argument the command needs; tests in `connector/command_login_test.go`. Remaining phase 2: `login password-server` flow (server as `host`, `host:port`, `tls://host:port`, `auto`) and the "used account X" notice.
  - Reference: `command_login.go`, `commands_login.go`.
- **WS4.3 Connection hardening** — phase 1 present: patched-xmpp performs DNS SRV lookup for `_xmpp-client` (`resolveSRVAddresses` in `patched-xmpp/client.go`) and falls back to the JID domain with port 5222; several candidate SRV addresses are tried. Remaining phase 2: `_xmpps-client`/XEP-0368 direct TLS, per-login server override, and the `network.allow_private_server_override` SSRF guard (incl. CGNAT; servername always verified against the JID domain).
  - Reference: `xmpp_dial.go`, `connector.go:26-42`.
- **WS4.4 Keepalive and connection health** — done phase 1+2: XEP-0199 runtime health ping every minute with 2 consecutive failures forcing reconnect (`startRuntimeHealth`/`RuntimeHealth`), but pings only fire when the stream was idle for 90 s (`recordServerActivity`/`serverIdleFor`, patched into the router handlers and `sendXMPP`); a 3-minute self-ping to our own full JID (`selfPingOwnJID`, XEP-0410 style half-open detection, auto-answered by the patched router) runs on the same loop; reconnect backoff 5 s doubling to 5 min, auth failures set permanent `bad_credentials` and wait for new credentials. Tests in `connector/xmpp_health_test.go`.
  - Reference: `xmpp_conn_health.go`.
- **WS4.5 XEP-0198 stream management** — done phase 1+2: stream resume is negotiated and used (`xmpp.IsStreamResumable`, resume-mode reconnect in `handleDisconnect`), outbound sends are tracked with ack timeouts (`trackOutboundSend`/`scanPendingSendTimeouts`), and message stanzas also store the exact packet (`trackOutboundStanza`); after a fresh (non-resumed) connect the bridge re-sends unacknowledged messages and notifies the management room (`resendPendingSends`, wired into the fresh-connect hook). Tests in `connector/network_client_test.go`.
- **WS4.6 MAM catch-up and backfill** — phase 1+2 present: automatic DM and MUC catch-up triggers after every connect (`triggerAutomaticDMCatchup`/`triggerAutomaticMUCCatchup`, `connector/history_sync.go`), on-demand backfill windows, and stale-history rejoin fallback. DM catch-up is already archive-wide: `scheduleDMBackfill("*")` → `backfillAccountDMs` pages the whole archive from the login-level `LastSyncAt` overlap and routes each item by peer, so numbers that texted while the bridge was offline still produce portals even if they are not in the roster. The default lookback is configurable via `history_days` (`SimpleConfig`, default 30 days; `historyLookback`, honoured by `sync` and `join-room` when no explicit duration is given, tests `TestHistoryLookback`/`TestParseSyncArgsWithFallback`/`TestParseJoinRoomArgsWithFallback`). Remaining optional polish: an RSM `after`-cursor variant instead of the timestamp window.
  - Reference: `xmpp_mam.go`, their To-do #8, ROADMAP "Backfill message ordering".
- **WS4.7 Config surface and polish** — phase 1+2 done: signal toggles `send_typing`, `send_read_markers`, `send_delivery_receipts` are wired through `SimpleConfig` (*bool, default enabled; `connector/naming.go`) and gate outbound chat states (`connector/xmpp_typing.go`) and receipt/marker extensions (`connector/handle_matrix.go`, `connector/xmpp_corrections.go`); tests in `connector/xmpp_signal_toggles_test.go`. `default_server`, `default_port`, and `resource_prefix` are now wired into the connection config (`xmppServerAddress`/`xmppJIDWithResource`, tests in `connector/xmpp_dial_test.go`). E2BE build documented and switched to pure-Go `goolm` (Dockerfile default build tags `goolm sipcalls nolibopusfile`, no libolm in the image; README "Build tags (calling support)"; existing libolm pickle v4 stores keep working). Remaining phase 2: tuning keys for dial timeouts/backoff/MAM interval if operators need them; strip credentials/JIDs from logs.
  - Reference: their CODE_OPTIMIZATION.md CFG-1..CFG-4, F-2, C-1.

### WS5 - Calls

- **WS5.1 SIP/MatrixRTC integration** — done phase 1 (opt-in by design): embedded `mrtc-sip-bridge` behind the `sipcalls` build tag (`connector/sipcalls*.go`, `connector/extension.go` seam): config `network.calls.*` (hostkit fields, library defaults/validation), per-login SIP account lifecycle (`sip-login`/`sip-logout`/`sip-status` with management-room prompt flow and auth-failure notice), core wiring (`core.RegisterMatrixRTCHandlers` from mxmain PostInit, `core.NewIngest`, `hostkit.NewCallStack` in Network.Start, sink switched to the calls manager), `core.CallHost` adapter (creates the Cheogram portal room when needed, joins the ghost, sets call power levels, posts call notices, marks calls read as owner) and XEP-0353 JMI notice suppression while the login's SIP account is registered (the SIP path rings instead). Default build unchanged; build calls with `go build -tags "sipcalls nolibopusfile"` (requires libopus headers at build time). Requirements to actually use: JMP self-service SIP credentials for the same number (JMP account settings; no third-party provider), LiveKit SFU + lk-jwt on the homeserver (`org.matrix.msc4143.rtc_foci` well-known or `network.calls.livekit_service_url`; `LIVEKIT_FULL_ACCESS_HOMESERVERS` must accept bridge ghosts), double puppeting, unencrypted rooms (encrypted portals: calls ring in a linked unencrypted call room created with `call-room`, or lazily with `network.calls.auto_create_call_room: true`). Remaining phase 2: live validation with a real SIP account + SFU, retry/backoff when the initial account start fails.
  - Reference: `sipcalls*.go`, `sipcalls_host.go`.
- **WS5.2 JMI notices** — done: XEP-0353 propose/retract/accept/reject/finish (plus ringing/proceed) are bridged as deterministic `m.notice` call-state messages in the DM (`connector/xmpp_call.go`: `extractXMPPCallEvent`, session state machine, `formatCallNoticeForParty`), works without a calls build tag; ids are `call-<action>-<session>`/XMPP message ids rather than the reference bridge's `jmi:<sid>:<action>`.
  - Reference: `xmpp_jingle.go`.
- **WS5.3 Dialpad surface** — done (phase 1+2). Phase 1: `sip-login`/`sip-logout`/`sip-status` (`connector/sipcalls_commands.go`) plus `call`/`hangup`/`dtmf` via `hostkit.RegisterCallCommands`; call rooms: `call-room <number>` / `call-room` / `call-room --remove <number>` manage a linked per-number unencrypted room where calls ring when the encrypted portal cannot (`connector/sipcalls_host.go`: `decideCallRoom`, `createCallRoomForPortal`, `portalForCallRoom` cache + scan, portal metadata `CallRoomMXID`; `calls.auto_create_call_room` for lazy creation). Phase 2: Dialpad widget — `calls.widget`/`pwa.*` config mirrored in `connector/calls_config.go` (CallsWidgetConfig/CallsWidgetPWAConfig), mapped to `hostkit.WidgetConfig` in `connector/sipcalls_config.go`; lifecycle in `connector/sipcalls_widget.go` (start/stop from `callsExt.start`/`stop`, lazy installer + dialpad-link adapters, `widgetResolveRoom`/`widgetResolveDevice`/`widgetEligibleRooms`, private rooms only; `widget` and `dialpad-link(s)`/`revoke` registered through `hostkit.RegisterCallCommands` CommandDeps; PWA device store via `widget.OpenDeviceStore`). Roster-only calls: rejection gate in `connector/sipcalls_accounts.go` onInvite (`phone.Normalize`, roster lookup, reject 480, rate-limited management notice) + `roster-only-calls [on|off|status]` command storing `LoginMetadata.CallRosterOnly` + widget `RosterOnlyPrefs` feature in `connector/sipcalls_widgetfeatures.go`. Call history: `LoginMetadata.CallHistory` + `connector/call_history.go` (`call-history [N]` command), JMI hook `recordJMICallHistory` (`connector/xmpp_call.go`), SIP hooks in `connector/sipcalls_history.go` (callAPIProxy PlaceCall → outgoing; onInvite → incoming). Tests: `connector/call_history_test.go`, `connector/sipcalls_test.go` (`TestCallsConfigWidgetToHostkit`); default + tagged variants green. Remaining: CardDAV onboarding polish (interactive `carddav-login` with connection test/confirmation) and live SIP/MatrixRTC validation.
  - Reference: `sipcalls_commands.go`, `sipcalls_widget*.go`; our `docs/CALL_BRIDGING_PLAN.md`, `docs/RUST_CHEOGRAM_CALL_CLIENT_SPEC.md`.
- **WS5.4 Ports/docs** — phase 1 done (docs): README documents the `sipcalls` build tag, config keys and requirements. Remaining phase 2 with live calls: publish SIP/RTP ports in the container image/playbook role when calls are enabled (Dockerfile installs libopus + builds with `-tags "sipcalls nolibopusfile"`).

## P1 - important phone-network polish

See also WS1.1 (typing), WS1.2/WS1.3 (edits/retracts), WS3.1 (avatars), WS2.5 (bookmarks), WS5 (calls).

### 7. Typing and chat states

Why:
- Typing indicators are visible in modern messaging and may survive through RCS/iMessage proxying.
- They matter more for this architecture than generic XMPP-only room features.

Backlog:
- map Matrix typing notifications to XMPP chat states
- parse inbound `composing`/`paused`/`active` chat states into Matrix typing events

Status: done (WS1.1) — `connector/xmpp_typing.go` + tests, gated by `send_typing` config.

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/xml/Namespace.java`
- `connector/handle_matrix.go`
- `connector/network_client.go`

### 8. Location and rich URI handling

Why:
- Location payloads are common in phone messaging ecosystems and currently degrade to plain text.

Backlog:
- parse location-style messages/URIs into Matrix location events or a stable fallback format
- ensure outbound Matrix locations map to something useful on XMPP/gateway side

Status: done phase 1 — `connector/xmpp_extras.go`: inbound `geo:` bodies become `m.location` events, outbound `m.location` sends the GeoURI as the message body (works through the gateway); richer outbound rendering may follow.

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/ui/text/FixedURLSpan.java`
- `cheogram-android/CHANGELOG.md`
- `connector/handle_matrix.go`

### 9. Stickers, inline images, and BOB/custom-emoji media

Why:
- These are common in iMessage/RCS-like ecosystems and can appear as image-ish payloads or custom emoji semantics.
- Even if full parity is impossible, predictable degradation matters.

Backlog:
- detect `cid:` / BOB-backed media and bridge it as Matrix media where practical
- decide how stickers should map to Matrix sticker or image events
- preserve custom emoji/image reactions where feasible

Status: done phase 1 — inbound XEP-0231 BOB payloads (`connector/xmpp_bob.go`) upload as Matrix media (png/jpeg/gif/webp and any other mime as a file, 5 MiB cap) and body-only `cid:` references degrade to a filename placeholder; image-based custom emoji reactions (`mxc://`) are stored in Matrix but not sent to XMPP (see P0 `#6`). Sticker decision: no dedicated sticker mapping — gateway traffic exposes no sticker type, so sticker-like images arrive as BOB/OOB media and already bridge through the normal image/file path. Remaining: decrypt-free `cid:` resolution decoupled from the carrying message.

Evidence:
- `cheogram-android/src/cheogram/java/com/cheogram/android/BobTransfer.java`
- `cheogram-android/src/cheogram/java/com/cheogram/android/InlineImageSpan.java`
- `cheogram-android/src/cheogram/java/com/cheogram/android/DownloadDefaultStickers.java`

### 10. Message edits, retracts, and moderation tombstones

Why:
- These matter if the proxy side can surface iMessage/RCS edit/unsend semantics, but they are less central than media and receipts for SMS/MMS-heavy flows.
- The bridge currently treats every inbound message as a new message and exposes edits as unsupported.

Backlog:
- implement Matrix -> XMPP message correction via `urn:xmpp:message-correct:0`
- parse inbound corrections and update existing Matrix events
- add retract/delete mapping for `urn:xmpp:message-retract:*`
- decide how to represent moderated/deleted XMPP messages on Matrix

Status: done (WS1.2/WS1.3) — `connector/xmpp_corrections.go`: outbound XEP-0308 corrections (Cheogram edits are sent as fresh `* ` messages), inbound corrections and XEP-0424/0425 retractions, SMS/MMS unsend attempts surface as a notice. Remaining phase 2: replay tombstone table, moderation authority checks, 3s MUC echo wait.

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/parser/MessageParser.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/ui/ConversationFragment.java`
- `cheogram-android/CHANGELOG.md`
- `connector/network_client.go`

### 11. Invite handling and stranger-invite policy

Why:
- This can affect phone-number onboarding and unexpected gateway-created chats, even if it is not a daily messaging feature.

Backlog:
- formalize behavior for direct invites, mediated invites, and unknown senders
- optionally expose a bridge setting/command for stranger invites

Status: done phase 1 (WS2.6) — direct/mediated MUC invites create the portal, are rate-limited to 10 invites/hour per login, and never auto-join anymore: invites surface as native Matrix invites (`connector/xmpp_muc_invites.go`, `PortalMetadata.MUCInvitePending`), accepting joins the MUC with the remembered password, rejecting forgets it. Remaining: live validation of the accept/reject round trip.

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/parser/MessageParser.java`
- `cheogram-android/CHANGELOG.md`
- `connector/network_client.go`

### 12. Service outage status and other account-level service signals

Why:
- For a gateway-backed messaging bridge, outage visibility is actually more useful than many pure-XMPP polish features.
- This is small-scope and operationally valuable.

Backlog:
- detect service-outage-status notifications
- surface them in bridge status / management room notices

Status: done phase 1 — `connector/xmpp_extras.go` routes XMPP headline messages with status text (e.g. gateway outage notices) to the management room as "Service status from …" notices instead of bridging them as chats. Deeper account-level outage state detection remains phase 2.

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/XmppConnection.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/ui/adapter/AccountAdapter.java`
- `cheogram-android/CHANGELOG.md`

## P2 - useful, but likely secondary for this architecture

### 13. Avatars and richer profile sync

Why:
- Nice to have, but not usually message-blocking for SMS/MMS gateway traffic.
- More important for contact polish than core delivery semantics.

Backlog:
- sync DM avatars into ghost profiles
- sync MUC/group avatars into portal rooms
- fetch and map display names/profile metadata from XMPP profile sources

Status: done (WS3.1/WS3.2) — vcard-temp 1:1 avatars (`connector/xmpp_avatar.go`), occupant contact-rule reuse for non-anonymous rooms, and XEP-0084 occupant avatars for anonymous rooms (`connector/xmpp_occupant_avatar.go`); `avatar-probe` diagnostic in `connector/xmpp_avatar_probe.go`. Remaining: group/portal avatars.

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/manager/AvatarManager.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/ui/PublishProfilePictureActivity.java`
- `cheogram-android/CHANGELOG.md`
- `connector/types.go`

### 14. Presence and last-active style signals

Why:
- Outside MUC occupant tracking, the bridge exposes almost no presence parity.
- Usually less important than receipts for phone-network bridging.

Backlog:
- bridge DM/contact presence into Matrix presence where appropriate
- expose status text or last-seen-style information if available
- decide privacy defaults for presence sync

Status: deferred by design (WS3.7) — privacy-first: XMPP presence is consumed internally (MUC joins, avatar hash triggers) but never bridged to Matrix and no Matrix presence is published. Revisit only on concrete request with a privacy story.

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/services/XmppConnectionService.java`
- `connector/network_client.go`

### 15. Room topics/subjects, public-channel distinction, and room metadata

Why:
- Mostly relevant for native XMPP rooms and channels, not the gateway-heavy phone-number path.

Backlog:
- sync room subject/topic into Matrix room topic/name updates
- preserve channel/private-group distinctions where possible
- evaluate join rules, visibility, and invite semantics for public rooms

Status: done phase 1 (WS2.7) — `handleIncomingMUCSubject`/`extractMUCSubject` turn subject-only groupchat stanzas into portal topic updates (`ChatInfoChange`). Remaining phase 2: disco#info room metadata on resync, clearing topics on empty subjects.

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/manager/MultiUserChatManager.java`
- `cheogram-android/CHANGELOG.md`
- `connector/handle_matrix.go`

### 16. Bookmarks and room persistence semantics

Why:
- Useful for native XMPP room lifecycle, but less critical for the SMS/MMS bridge path.

Backlog:
- sync bookmark add/remove/autojoin state
- map Matrix room state or bridge commands to native XMPP bookmark operations
- support native bookmark variants consistently

Status: done phase 1+2 (WS2.5) — `connector/xmpp_bookmarks.go` publishes XEP-0402 bookmarks on group creation/join and (with `bookmarks-sync [push]`) republishes known bookmarks; `connector/xmpp_bookmarks_pep.go` applies live PEP events (autojoin on → join/import, off/retract → leave + cleanup, leaving marks autojoin off); `requestBookmarks`/`requestBookmarks2` fetch XEP-0402 and legacy private storage. Remaining: optional XEP-0049 legacy import (merge/gating/backfill done in `connector/xmpp_bookmarks_merge.go`).

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/manager/BookmarkManager.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/manager/NativeBookmarkManager.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/manager/LegacyBookmarkManager.java`
- `connector/network_client.go`

## P3 - likely out of scope unless the gateway path proves it matters

### 17. webxdc handling or explicit degradation strategy

Why:
- Interesting, but not central to Matrix <-> SMS/MMS.
- A graceful fallback is probably enough unless the gateway starts carrying it in practice.

Backlog:
- detect webxdc payloads and updates
- choose a strategy: native passthrough, attachment-only fallback, or notice + file preservation
- preserve update-thread linkage if partial support is attempted

Evidence:
- `cheogram-android/src/cheogram/java/com/cheogram/android/WebxdcPage.java`
- `cheogram-android/src/cheogram/java/com/cheogram/android/WebxdcUpdate.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/parser/MessageParser.java`

Status: deferred (P3) — no webxdc detection yet; unknown payloads ride the normal body/media path and degrade to text or a notice. Revisit only if gateway traffic starts carrying webxdc in practice.

### 18. Calls and Jingle

Why:
- Cheogram has deep call integration, but this bridge's core problem is messaging parity through a phone-network gateway.
- Promoted to WS5 now that `mrtc-sip-bridge` exists; keep Jingle parity itself out of scope.

Backlog:
- adopt WS5 (SIP/MatrixRTC via mrtc-sip-bridge), do not implement raw Jingle
- define Matrix call event mapping and minimum viable subset for the adopted path
- document unsupported behavior and graceful fallbacks

Evidence:
- `cheogram-android/src/cheogram/java/com/cheogram/android/ConnectionService.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/JingleRtpConnection.java`
- `cheogram-android/CHANGELOG.md`

Status: WS5 — call-state Jingle messages bridge as `m.notice` (WS5.2, done). SIP/MatrixRTC calling ships in the standard image and is available by default (WS5.1/WS5.3 done incl. call rooms, Dialpad widget, roster-only calls and call history; WS5.4 docs done): see the Workstreams section; `network.calls.enabled: false` disables the SIP runtime entirely and each account opts in with `sip-login`. Encrypted portal rooms use the interim call-room model: `calls.auto_create_call_room` now defaults to `true`, call rooms are named `📞 <contact>`, hidden from `list-rooms`, and mirror their notices into the portal; the planned follow-up for ringing directly in encrypted rooms is the audio-only SFrame/E2EE spike in `docs/SFRAME_CALLS_SPIKE.md`. Live validation with a real SIP account + SFU is the remaining step. Raw Jingle stays out of scope.

### 19. Advanced file transfer modes beyond HTTP upload

Why:
- Valuable only if the gateway path actually produces these transfer modes in practice.
- For SMS/MMS use, HTTP-upload-style normalization is probably enough most of the time.

Backlog:
- identify which non-HTTP transfers can be normalized into Matrix media reliably
- add explicit fallback behavior when only P2P/Jingle transfer is available

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/JingleFileTransferConnection.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/transports/WebRTCDataChannelTransport.java`
- `cheogram-android/CHANGELOG.md`

Status: satisfied phase 1 — XEP-0363 outbound upload plus XEP-0066 OOB inbound download cover the gateway paths; download/upload failures keep a readable link fallback, so P2P/Jingle-only transfers are not required. Revisit only if the gateway starts sending Jingle file transfers.

### 20. Registration, onboarding, and account-management extras

Why:
- Useful, but secondary unless you want the bridge itself to own the entire JMP onboarding lifecycle.

Backlog:
- support account registration or bootstrap flows if bridge scope includes first-run onboarding
- evaluate CAPTCHA, invite-token, and gateway provisioning support
- add account-management operations beyond login/logout where useful

Evidence:
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/manager/RegistrationManager.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/utils/EasyOnboardingInvite.java`
- `cheogram-android/CHANGELOG.md`
- `connector/login.go`

Status: out of scope by design — JMP account creation stays on the JMP website; the bridge offers `password`/`password-server` login flows (`connector/login.go`), gateway-side registration and account commands through `gateway-actions`/`gateway-form`/`gateway-submit`, and contact adds via `add-to-roster`/`save-contact`/CardDAV. CAPTCHA/invite-token onboarding is a JMP product concern, not bridge scope.

### 21. OMEMO (XEP-0384)

Why:
- XMPP-side E2EE; orthogonal to gateway traffic, but big for native XMPP contacts.

Backlog:
- decrypt/encrypt in message and inbound paths; key storage; DM first, MUC later
- until then keep WS1.8 degradation behavior (notice, ids preserved)

Evidence:
- their ROADMAP Phase 4 / To-do #5; `xmpp_subject_encrypted.go`

Status: deferred — no OMEMO decryption/encryption is implemented. Encrypted payloads (OMEMO, OpenPGP, XEP-0380 EME, `aesgcm://`) degrade to a fixed `m.notice` with ids preserved (WS1.8, `connector/xmpp_encrypted.go`) so replies/corrections/retractions still map; key-transport messages surface as the same notice instead of being dropped.

## Suggested implementation order

P0 gateway work stays the top priority. Workstreams are ordered inside it:

1. gateway/PSTN command and form flows (P0 `#1`)
2. MMS-style media fidelity: captions, subject lines, metadata, graceful fallbacks (P0 `#2`)
3. WS1 messaging parity (start: WS1.1 typing, WS1.2/WS1.3 corrections/retractions, WS1.7 ids/dedupe)
4. receipts and displayed-state sync (P0 `#3` + WS1.4/WS1.5)
5. replies (P0 `#5`)
6. finish reactions parity (P0 `#6` + WS1.4)
7. WS2 MUC robustness (join health, nicks, auto-rejoin, bookmarks, invites, topic, group-model validation)
8. WS3 identity/contacts (avatars, roster commands, CardDAV, roster isolation)
9. WS4 ops/reliability (OTEL, multi-account, connection hardening, MAM catch-up, config surface)
10. WS5 calls
11. location/sticker/custom-media degradation strategy (`#8`/`#9`)
12. avatars/presence/room metadata only where they improve real gateway conversations
13. defer webxdc, advanced Jingle transfer, broad XMPP-client parity, OMEMO (`#17`/`#19`/`#21`) unless they become gateway-relevant

## Concrete execution plan for the original top 5

### A. Gateway/PSTN command and form flows

Goal:
- make the Matrix management room usable for the gateway actions that Cheogram users normally trigger through ad-hoc commands, forms, and gateway prompts

Concrete tasks:
- add XMPP disco/feature detection for `http://jabber.org/protocol/commands` and `jabber:iq:gateway`
- add a command-discovery path in the bridge management room, for example `commands`, `gateway-info`, or `service-actions`
- add a minimal form renderer for `jabber:x:data` fields into Matrix notices or command-driven prompts
- add form submission back to XMPP IQ set flows
- prioritize only gateway registration, phone lookup, add-contact, and account/service actions first; defer generic arbitrary-command UX

Probable Matrix/XMPP mapping:
- Matrix management-room commands or notices -> XMPP disco/info + ad-hoc command IQs
- Matrix reply/command arguments -> XMPP data-form submission
- gateway prompts -> Matrix notices with structured follow-up commands

Primary code entry points:
- `connector/commands.go` for user-facing bridge commands
- `connector/network_client.go:1413` for IQ result handling and command/gateway disco responses
- `connector/my_connector.go` for registering any new management commands

Acceptance shape:
- a user can discover supported gateway actions from Matrix
- a user can complete at least one real gateway flow without leaving Matrix

First milestone:
- `A1. Gateway action discovery + one usable form round-trip`

File-by-file sub-tasks:
- `connector/my_connector.go`: register one or two new management commands for gateway/service discovery
- `connector/commands.go`: add a user-facing command surface for listing service actions and submitting simple form answers
- `connector/network_client.go`: extend IQ disco handling to cache command/gateway features and process one command result flow
- `connector/network_client.go`: add outbound IQ helpers for ad-hoc command execution and `jabber:x:data` form submission
- `connector/types.go` or a new helper file: store any lightweight pending-form/session metadata needed to correlate Matrix replies with XMPP form submissions

Definition of done for A1:
- from the management room, a user can list at least one real gateway action
- the bridge can render a simple form with text/list fields and submit the response successfully
- the flow is documented in management-room help text

### B. MMS-style media fidelity: captions, subjects, metadata, graceful fallbacks

Goal:
- make media conversations look like phone-network conversations instead of bare file relays

Concrete tasks:
- preserve outgoing Matrix caption/body text alongside OOB media instead of collapsing everything into plain body + URL fallback only
- decide and document how XMPP `subject` maps to Matrix for DM/gateway traffic
- preserve incoming attachment metadata already exposed by SIMS parsing: filename, MIME type, size, width/height, duration
- add tests for media-plus-text, subject-plus-media, and failed-download fallback cases
- evaluate whether Matrix `FileName`/caption/body should be split differently for audio vs generic files vs images

Probable Matrix/XMPP mapping:
- Matrix `m.image`/`m.video`/`m.audio`/`m.file` + `body`/`filename` -> XMPP OOB + fallback body + optional subject/description
- XMPP SIMS/reference + body/subject -> Matrix media event with caption/body and `info` metadata
- failed remote fetch -> Matrix text notice or text-with-link fallback, never invalid media event

Primary code entry points:
- `connector/handle_matrix.go:25` for Matrix -> XMPP media sending
- `connector/network_client.go:1876` and `connector/network_client.go:2051` for incoming XMPP -> Matrix conversion
- `connector/xmpp_attachment.go:25` and `connector/xmpp_message_extensions.go:28` for attachment metadata parsing

Acceptance shape:
- voicemail, MMS image+caption, and file+description messages round-trip with readable captions and valid Matrix media events

### C. Receipts and displayed-state sync

Goal:
- stop read/delivery state from drifting between Matrix and the gateway side

Concrete tasks:
- parse inbound `urn:xmpp:receipts` and `urn:xmpp:chat-markers:0` stanzas into bridge receipt events instead of ignoring them
- add outbound Matrix read-marker handling that emits XMPP receipt/displayed markers for supported chats
- attach `markable` / receipt-request extensions on outbound XMPP messages where appropriate
- decide what to do for DM vs MUC semantics, especially if only DMs should emit displayed markers
- add logging and tests around receipt target-ID resolution

Probable Matrix/XMPP mapping:
- Matrix read receipt / fully-read marker -> XMPP `displayed` or `received` marker and/or receipt ack
- XMPP `received` -> Matrix delivery/read-style acknowledgement where bridgev2 supports it
- XMPP `displayed` -> Matrix read receipt for the mapped event

Primary code entry points:
- `connector/xmpp_message_extensions.go:129` for receipt and marker structs
- `connector/handle_matrix.go:25` for attaching outbound receipt/markable extensions and adding read-marker support
- `connector/network_client.go:1739` and `connector/network_client.go:1876` for inbound message/marker parsing

Acceptance shape:
- reading a DM on Matrix updates the XMPP/gateway side
- inbound XMPP displayed markers advance Matrix read state for the correct event

First milestone:
- `C1. DM receipt parity for displayed + delivery markers`

File-by-file sub-tasks:
- `connector/handle_matrix.go`: implement `bridgev2.ReadReceiptHandlingNetworkAPI` and send outbound displayed markers for bridged DMs
- `connector/handle_matrix.go`: attach receipt-request / markable extensions to outbound DM messages so the remote side can acknowledge them
- `connector/xmpp_message_extensions.go`: parse receipt and marker IDs instead of treating them as opaque inner XML
- `connector/network_client.go`: recognize inbound `received` and `displayed` marker stanzas before empty-body filtering
- `connector/network_client.go`: queue `simplevent.Receipt` events with correct portal, sender, target message IDs, and timestamps
- `connector/*_test.go`: add unit coverage for receipt-target extraction or receipt classification helpers

Definition of done for C1:
- outgoing Matrix DM messages request delivery/read acknowledgements
- Matrix read receipts emit XMPP displayed markers for known bridged messages
- inbound XMPP delivery/displayed markers update Matrix delivery/read state for the matching bridged event
- unsupported or unresolvable receipt targets are ignored cleanly without breaking message handling

### D. Replies

Goal:
- preserve conversational context when the downstream path supports quote/reply semantics, while degrading cleanly for SMS/MMS-style fallback text

Concrete tasks:
- inspect Matrix reply relations in `HandleMatrixMessage` and map them to XMPP reply metadata plus fallback body text
- parse inbound XMPP reply/fallback payloads and build Matrix reply relations instead of treating them as plain text
- avoid double-rendering quoted fallback text when both reply metadata and body text are present
- decide the minimum viable behavior for gateways that flatten replies to quoted text only

Probable Matrix/XMPP mapping:
- Matrix `m.relates_to -> m.in_reply_to` -> XMPP `urn:xmpp:reply:0` + `urn:xmpp:fallback:0`
- XMPP reply reference + fallback range -> Matrix reply relation with fallback body stripped from visible text
- gateway-quoted plain text only -> Matrix plain text fallback when no reliable target ID exists

Primary code entry points:
- `connector/handle_matrix.go:25` for outgoing Matrix reply conversion
- `connector/network_client.go:1876` and `connector/network_client.go:2051` for incoming reply parsing and event mapping
- `connector/xmpp_message_extensions.go` for any new reply/fallback structs beyond the current OOB fallback support

Acceptance shape:
- replying to a message in Matrix produces a recognizable reply on the XMPP/gateway side
- incoming Cheogram replies become Matrix replies when target IDs are resolvable

### E. Finish reactions parity

Goal:
- make reactions reliable enough for RCS/iMessage-style proxy traffic and predictable for plain SMS fallback cases

Concrete tasks:
- implement Matrix reaction removal instead of the current no-op
- validate target-ID mapping for DMs, MUCs, carbons, and MAM history
- decide how to handle multi-reaction updates if XMPP sends more than one reaction value
- document and test the fallback behavior for unsupported custom emoji/image reactions

Probable Matrix/XMPP mapping:
- Matrix reaction add -> XMPP `urn:xmpp:reactions:0`
- Matrix reaction remove -> XMPP empty/update semantics if supported, otherwise bridge-side removal only with clear degradation behavior
- XMPP reactions -> Matrix annotations tied to the bridged target event

Primary code entry points:
- `connector/handle_matrix.go:113` for outgoing reactions and removal handling
- `connector/network_client.go:2171` for inbound XMPP reaction conversion
- `connector/network_client.go:2239` for message-ID extraction correctness, which reactions depend on

Acceptance shape:
- add/remove reaction works in Matrix without leaving stale bridge state
- inbound gateway/Cheogram reactions attach to the correct Matrix event across live and backfilled traffic
