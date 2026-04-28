# TODO

Bridge parity backlog derived from a deep scan of `cheogram-android/` against the current `mautrix-jmpchat` connector.

## Architecture note

This backlog is for a bridge path closer to:

`Matrix <-> XMPP/Cheogram/Snikket <-> cheogram/jmp.chat gateway bridge <-> SMS/MMS <-> phones (and sometimes iMessage/RCS by proxy)`

That changes priorities.

- Highest priority is not general XMPP client parity; it is preserving the semantics that survive the phone-network gateway.
- Features that matter most are gateway workflows, phone-number identity, MMS-style media fidelity, receipts/status, and anything likely to round-trip through SMS/MMS or gateway-mediated iMessage/RCS.
- Features that are mostly native-XMPP product polish should be deprioritized unless the gateway actually exposes them in a useful way.

## Current baseline

Already covered well enough to avoid duplicating here:

- basic XMPP login and login validation
- DM and group portal creation/sync
- MAM backfill and bookmark-driven room discovery
- text messages in both directions
- basic media/file bridging via HTTP upload/OOB/SIMS
- basic reactions in both directions

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

## P1 - important phone-network polish

### 7. Typing and chat states

Why:
- Typing indicators are visible in modern messaging and may survive through RCS/iMessage proxying.
- They matter more for this architecture than generic XMPP-only room features.

Backlog:
- map Matrix typing notifications to XMPP chat states
- parse inbound `composing`/`paused`/`active` chat states into Matrix typing events

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

### 18. Calls and Jingle

Why:
- Cheogram has deep call integration, but this bridge's core problem is messaging parity through a phone-network gateway.
- This is probably a separate product effort, not a near-term backlog item.

Backlog:
- decide whether call parity is in scope for this bridge
- if yes, define Matrix call event mapping and minimum viable Jingle subset
- if no, explicitly document unsupported behavior and graceful fallbacks

Evidence:
- `cheogram-android/src/cheogram/java/com/cheogram/android/ConnectionService.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/JingleRtpConnection.java`
- `cheogram-android/CHANGELOG.md`

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

## Suggested implementation order

1. gateway/PSTN command and form flows
2. MMS-style media fidelity: captions, subject lines, metadata, graceful fallbacks
3. receipts and displayed-state sync
4. replies
5. finish reactions parity
6. typing indicators
7. location/sticker/custom-media degradation strategy
8. edits/retracts if the gateway path actually preserves them
9. avatars/presence/room metadata only where they improve real gateway conversations
10. defer calls, webxdc, advanced Jingle transfer, and broad XMPP-client parity unless they become gateway-relevant

## Concrete execution plan for the top 5

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
