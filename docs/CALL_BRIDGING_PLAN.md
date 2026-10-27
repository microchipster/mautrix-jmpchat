# Audio Call Bridging Plan

This document is a design note for a future implementation of Cheogram/JMP audio calls in Matrix. It intentionally does not describe video calls, Matrix group calls, or media proxying as initial goals.

The target use case is phone replacement: a user logged into this bridge with a JMP/Cheogram XMPP account should be able to receive and place audio calls from Matrix clients.

## Scope

Implement only 1:1 audio calls between Matrix rooms and Cheogram/JMP XMPP contacts.

In scope:

- Incoming JMP/Cheogram audio calls should ring in the corresponding Matrix DM portal.
- Matrix users should be able to decline, answer, and hang up calls.
- Matrix users should be able to place outgoing audio calls to JMP/Cheogram contacts.
- The bridge should translate signaling only; it should not proxy RTP media in the first implementation.
- The bridge should support direct WebRTC media negotiation between the Matrix client and the Cheogram/JMP side using ICE, DTLS, and SRTP.

Out of scope for the first implementation:

- Video calls.
- Matrix group calls.
- Multi-party conference calls.
- Media proxying, transcoding, recording, or RTP anchoring.
- Content-add video upgrades.
- File-transfer Jingle sessions.
- OMEMO DTLS verification as a hard requirement. Preserve room to add it later, but do not block initial audio calling on it.

If any remote XMPP call advertises video, either ignore the video content and continue with audio if that is safe, or reject the call with a clear unsupported-media reason. Do not silently bridge video as audio.

## Current Bridge State

The bridge already has call-notice handling in `connector/xmpp_call.go`.

Current behavior:

- `extractXMPPCallEvent` detects XMPP Jingle Message Initiation events such as `propose`, `ringing`, `proceed`, `accept`, `reject`, `retract`, and `finish`.
- `advanceXMPPCallSession` tracks a lightweight call notice state machine.
- `handleIncomingXMPPCall` creates Matrix notices such as "Incoming call", "Calling Alice", "Call ended", and "Missed call".
- Existing tests in `connector/xmpp_call_test.go` cover call notice parsing and formatting.

This is not interactive calling. It is only call history/status rendering.

The future implementation should preserve this notice behavior for clients that cannot handle Matrix call events and for historical call status entries.

## Cheogram Source Of Truth

Cheogram Android is the behavior reference. The important files are:

- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/JingleConnectionManager.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/JingleRtpConnection.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/RtpContentMap.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/SessionDescription.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/stanzas/RtpDescription.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/stanzas/IceUdpTransportInfo.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/stanzas/Content.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/stanzas/Reason.java`

Important Cheogram behavior observed during the audit:

- Incoming Jingle IQs are routed by `JingleConnectionManager.deliverPacket`.
- RTP calls use Jingle RTP with namespace `urn:xmpp:jingle:apps:rtp:1` and ICE-UDP transport.
- Call discovery/ringing starts with XEP-0353 Jingle Message Initiation, not immediately with a Jingle IQ in the common path.
- `propose` messages advertise RTP descriptions and media types.
- If the recipient accepts, the recipient sends `proceed`, then the initiator sends a Jingle `session-initiate` IQ.
- The responder replies with Jingle `session-accept`.
- ICE candidates are exchanged through Jingle `transport-info` IQs.
- Hangup/failed calls use Jingle `session-terminate` plus Jingle Message Initiation `finish` messages.
- Cheogram has busy, stranger, tie-break, and multi-device logic. The bridge does not need all of that on day one, but it must avoid doing obviously wrong things when duplicate devices or crossed calls occur.

Cheogram's outgoing flow is visible in `JingleConnectionManager.proposeJingleRtpSession` and `JingleRtpConnection.sendSessionInitiate`.

Cheogram's incoming flow is visible in `JingleConnectionManager.deliverMessage`, `JingleConnectionManager.deliverPacket`, `JingleRtpConnection.receiveSessionInitiate`, and `JingleRtpConnection.sendSessionAccept`.

## Protocols Involved

### XMPP Side

The bridge needs support for these XMPP protocols:

- XEP-0353 Jingle Message Initiation.
- XEP-0166 Jingle base protocol.
- XEP-0167 Jingle RTP Sessions.
- XEP-0176 Jingle ICE-UDP Transport.
- XEP-0320 DTLS-SRTP, including DTLS fingerprints.

The existing bridge already has some XEP-0353 message payload structs for call notices. The interactive implementation needs full Jingle IQ payload structs as well.

Relevant XMPP events:

- Message `propose`: remote device is offering an audio call.
- Message `ringing`: callee device is ringing.
- Message `proceed`: callee accepted the proposal and is ready for the Jingle session.
- Message `reject`: callee declined before a Jingle session was established.
- Message `retract`: caller cancelled before a Jingle session was established.
- Message `finish`: call finished or failed.
- IQ `session-initiate`: Jingle offer with RTP description, DTLS fingerprint, ICE credentials, and optionally early candidates.
- IQ `session-accept`: Jingle answer.
- IQ `transport-info`: additional ICE candidate.
- IQ `session-terminate`: call ended after a Jingle session was established.

### Matrix Side

The realistic initial target is legacy Matrix 1:1 VoIP events:

- `m.call.invite`
- `m.call.answer`
- `m.call.candidates`
- `m.call.hangup`

Mautrix already has Go types in `maunium.net/go/mautrix/event/voip.go`:

- `event.CallInviteEventContent`
- `event.CallAnswerEventContent`
- `event.CallCandidatesEventContent`
- `event.CallHangupEventContent`
- `event.CallData`
- `event.CallCandidate`

Do not start with Element Call or Matrix group-call semantics. They are a different signaling model and would expand the project significantly.

## Architecture

Add an explicit call signaling layer instead of growing `connector/xmpp_call.go` into a monolith.

Suggested files:

- `connector/xmpp_call.go`: keep existing call notice/state helpers.
- `connector/xmpp_jingle.go`: XMPP Jingle IQ payload structs and XML helpers.
- `connector/xmpp_jingle_sdp.go`: conversion between Jingle RTP content and SDP.
- `connector/matrix_call.go`: Matrix `m.call.*` parsing and sending helpers.
- `connector/call_session.go`: per-call state machine tying Matrix call IDs to XMPP session IDs.

The core model should be a per-login in-memory call session registry owned by `MyNetworkClient`.

Suggested state fields:

```go
type bridgedCallSession struct {
    MatrixRoomID id.RoomID
    MatrixCallID string
    MatrixPartyID string

    XMPPPeerBareJID string
    XMPPPeerFullJID string
    XMPPSessionID string

    Direction callDirection
    State callState
    Media callMedia

    CreatedAt time.Time
    UpdatedAt time.Time

    MatrixOfferSDP string
    MatrixAnswerSDP string
    XMPPInitiatorContent *rtpContentMap
    XMPPResponderContent *rtpContentMap

    PendingMatrixCandidates []event.CallCandidate
    PendingXMPPCandidates []jingleCandidate
}
```

Keep this state in memory at first. Calls do not survive bridge restarts well because WebRTC state is also lost. On reconnect or restart, fail active calls cleanly with `connectivity-error` or Matrix hangup `unknown_error`/`ice_failed` as appropriate.

## Direction: Incoming XMPP Call To Matrix

### Phase 1: Ring And Decline Only

This is the safest first vertical slice.

Flow:

1. Receive XMPP `propose` message with RTP audio description.
2. Validate it is 1:1, not MUC, and only audio.
3. Create or find the DM portal for the caller.
4. Create a `bridgedCallSession` with the XMPP session ID and peer JID.
5. Send Matrix `m.call.invite` if possible, or a Matrix notice if the selected Matrix client path does not support calls yet.
6. If the Matrix user declines/hangups before answering, send XMPP `reject` or `retract` depending on which side initiated.
7. If the XMPP side sends `retract` or `finish`, send Matrix `m.call.hangup`.

Open question: Matrix `m.call.invite` normally contains an SDP offer. A JMI `propose` does not contain enough SDP to create a real Matrix call invite. Therefore a pure ring-without-media phase may need either a notice plus management command, or a synthetic invite that cannot be answered. A better first real VoIP slice may be to wait for XMPP `session-initiate`, because that includes the offer.

Practical recommendation:

- Keep existing Matrix notices for `propose`.
- Add Matrix decline/hangup handling for pending XMPP proposals first.
- Do not send a real Matrix `m.call.invite` until an SDP offer can be constructed from `session-initiate`.

### Phase 2: Answer With Audio

Full incoming flow:

1. Receive XMPP `propose`.
2. Show call notice or pending-call UI state in Matrix.
3. If Matrix user accepts, send XMPP `proceed` to the proposer.
4. Receive XMPP `session-initiate` IQ.
5. Convert Jingle RTP content to SDP offer.
6. Send Matrix `m.call.invite` containing the SDP offer.
7. Receive Matrix `m.call.answer` containing SDP answer.
8. Convert SDP answer to Jingle RTP content.
9. Send XMPP `session-accept` IQ.
10. Translate Matrix `m.call.candidates` into XMPP `transport-info` IQs.
11. Translate XMPP `transport-info` IQs into Matrix `m.call.candidates`.
12. Translate hangups both ways.

The awkward part is steps 2-6. In Matrix, the invite starts the ringing UI and includes the SDP offer. In Cheogram/Jingle, JMI `propose` starts ringing before the full Jingle offer exists. Cheogram sends the full offer only after the callee sends `proceed`.

Possible strategy:

- For incoming XMPP calls, initially present a Matrix notice with explicit text: "Incoming call from +1555...; answering from Matrix is not implemented yet".
- Once implementing answer, use a bridge command or custom UI fallback to send `proceed` and trigger `session-initiate`, then send the Matrix invite. This may ring late from the Matrix client's perspective.
- If Matrix clients require a normal invite-first flow, the bridge may need to auto-send `proceed` when an audio `propose` arrives, receive `session-initiate`, and then send Matrix `m.call.invite`. This means the XMPP side sees the bridge as having accepted enough to start WebRTC negotiation before the Matrix user actually answers. That may be acceptable if the bridge can quickly send `session-terminate` when the Matrix user declines, but it should be tested carefully against Cheogram.

## Direction: Outgoing Matrix Call To XMPP

Outgoing calls are cleaner because Matrix starts with an SDP offer.

Flow:

1. Matrix user starts an audio call in a portal room.
2. Bridge receives `m.call.invite` with SDP offer.
3. Validate the portal is a 1:1 DM and the offer is audio-only.
4. Create an XMPP session ID and call session.
5. Send XMPP JMI `propose` with audio RTP description to the contact bare JID.
6. Wait for XMPP `ringing`, `proceed`, `reject`, or `finish`.
7. On `ringing`, optionally send Matrix feedback. Matrix legacy calls do not have a direct ringing event in the same way, so this may remain a notice or no-op.
8. On `proceed`, convert the Matrix SDP offer to Jingle RTP content and send XMPP `session-initiate` IQ.
9. Receive XMPP `session-accept`, convert it to SDP answer, and send Matrix `m.call.answer`.
10. Translate candidates and hangups both ways.

This likely should be the first real media milestone after basic parsing because it maps better to Matrix's invite-first model.

## SDP To Jingle Conversion

The conversion layer is the hardest part.

Cheogram already has this in Java. The equivalent Go implementation should be narrowly scoped to audio-only first.

From Matrix SDP offer/answer, parse:

- ICE username fragment.
- ICE password.
- DTLS fingerprint hash and fingerprint.
- DTLS setup role: `actpass`, `active`, or `passive`.
- RTP media kind; accept only `audio` initially.
- RTP payload types.
- Codec names, clock rates, and channels.
- FMTP parameters.
- RTCP feedback lines.
- SSRC and optional media stream identifiers if needed by clients.
- ICE candidates from either SDP or later `m.call.candidates`.

To XMPP Jingle, produce:

- `<jingle xmlns='urn:xmpp:jingle:1' action='session-initiate|session-accept|transport-info|session-terminate' sid='...'>`
- `<content creator='initiator|responder' name='audio'>`
- `<description xmlns='urn:xmpp:jingle:apps:rtp:1' media='audio'>`
- `<payload-type id='...' name='opus' clockrate='48000' channels='2'>`
- `<parameter name='...' value='...'>` for FMTP when needed.
- `<rtcp-fb type='...' subtype='...'>` when needed.
- `<transport xmlns='urn:xmpp:jingle:transports:ice-udp:1' ufrag='...' pwd='...'>`
- `<fingerprint xmlns='urn:xmpp:jingle:apps:dtls:0' hash='sha-256' setup='actpass|active|passive'>...`
- `<candidate ...>` entries for ICE candidates.

From XMPP Jingle, produce SDP offer/answer that Matrix clients accept:

- `v=0`
- `o=- ...`
- `s=-`
- `t=0 0`
- `a=group:BUNDLE audio` if using bundle.
- `m=audio ... RTP/SAVPF ...`
- `c=IN IP4 0.0.0.0` or equivalent placeholder.
- `a=mid:audio`
- `a=sendrecv`
- `a=rtcp-mux`
- `a=ice-ufrag:...`
- `a=ice-pwd:...`
- `a=fingerprint:sha-256 ...`
- `a=setup:actpass|active|passive`
- payload, fmtp, rtcp-fb, and candidate lines.

Do not try to support every SDP feature. Start with the minimal subset observed between Cheogram and the Matrix clients we care about.

## ICE Candidate Translation

Matrix candidates use `event.CallCandidate`:

```go
type CallCandidate struct {
    Candidate string `json:"candidate"`
    SDPMLineIndex int `json:"sdpMLineIndex"`
    SDPMID string `json:"sdpMid"`
}
```

The `Candidate` string is normally an SDP `candidate:` attribute. Cheogram has parsing logic in `IceUdpTransportInfo.Candidate.fromSdpAttribute`.

Implementation guidance:

- Preserve `sdpMid` as the Jingle content name.
- For audio-only, expect `sdpMid` to be `audio` or a single media index.
- Parse SDP candidate strings into XMPP candidate attributes.
- Convert XMPP candidate attributes back into SDP candidate strings for Matrix.
- Buffer candidates that arrive before the other side's content map is ready.
- Drop duplicate candidates by foundation/component/protocol/ip/port/type or by exact candidate string.
- Ignore end-of-candidates at first unless the Matrix clients require it.

## DTLS Role Mapping

DTLS setup role is easy to get subtly wrong.

General mapping:

- Offer usually uses `actpass`.
- Answer chooses `active` or `passive`.
- Jingle fingerprints carry a `setup` attribute.

Use Cheogram's `RtpContentMap.requireDTLSFingerprint` and `IceUdpTransportInfo.Setup` behavior as reference. If calls connect but media never flows, DTLS role mismatch is one of the first things to inspect.

## Call State Machine

Do not implement calls as loose if-statements. Use an explicit state machine.

Suggested states:

- `new`
- `xmpp_proposed`
- `matrix_invited`
- `xmpp_proceed_received`
- `xmpp_session_initiated`
- `matrix_answer_received`
- `xmpp_session_accepted`
- `connected`
- `terminating`
- `terminated`
- `failed`

Suggested terminal reasons:

- `declined`
- `busy`
- `timeout`
- `cancelled`
- `success`
- `connectivity_error`
- `unsupported_media`
- `bad_remote_description`
- `bridge_restart`

The state machine needs to handle duplicates and out-of-order events. XMPP and Matrix can both retry or deliver late candidates.

Rules to enforce:

- One active audio call per XMPP login initially. This matches Cheogram's conservative busy behavior and is much simpler.
- Reject new incoming calls with XMPP `busy` when another active call exists.
- Reject or ignore Matrix call invites in a portal if another call is active.
- Ignore candidate events after terminal state.
- Send idempotent hangup/terminate responses when duplicate terminal events arrive.
- Time out pending proposals and pending session negotiation.

## Matrix Event Injection

Before coding, verify how bridgev2 wants network connectors to inject non-message Matrix events. Existing `simplevent.Message` conversion is oriented around chat messages. Calls are room events but not message bodies.

Possible approaches:

- Use the Matrix intent/client directly to send `event.CallInvite`, `event.CallAnswer`, `event.CallCandidates`, and `event.CallHangup` into the portal room.
- Add a custom remote event type if bridgev2 supports it cleanly.
- Use `ConvertedMessagePart` only if mautrix/bridgev2 actually allows non-message event types in this path. Do not assume it does.

This should be researched with a tiny spike before implementing the whole call stack.

The spike should send a hardcoded `m.call.invite` into a test portal room and confirm that the target Matrix client displays a call UI.

## Matrix Event Handling

The bridge currently handles Matrix text/media/reaction/read receipt style events in `connector/handle_matrix.go` and related files. The call implementation needs to detect Matrix call events in portal rooms and route them to the per-login call session.

Handle at least:

- `event.CallInvite`: outgoing Matrix call started.
- `event.CallAnswer`: Matrix user answered an incoming or outgoing call flow.
- `event.CallCandidates`: Matrix client sent ICE candidates.
- `event.CallHangup`: Matrix user hung up or call failed.

Reject calls from non-portal rooms. Direct Matrix rooms that are not bridge portals do not route through `HandleMatrixMessage`; this is already an important bridge invariant.

## XMPP IQ Routing

Interactive calls require routing Jingle IQs, not only messages.

Needed work:

- Add Jingle IQ extension structs to the XMPP stanza registry.
- Register handlers for incoming Jingle IQs in `MyNetworkClient` after connect.
- Route by session ID and remote full JID/bare JID.
- Send proper IQ result acknowledgements for accepted Jingle actions.
- Send Jingle errors for malformed, unknown-session, unsupported-info, and out-of-order actions.

Cheogram behavior to mimic:

- Unknown session -> Jingle error `unknown-session` plus stanza `item-not-found`.
- Missing action/session ID -> bad request.
- Unsupported media/transport -> feature-not-implemented or unsupported-info.
- Busy -> acknowledge incoming request where required, then send session terminate with reason `busy`.

## Feature Discovery

Eventually the bridge should advertise the right capabilities on XMPP if it has a disco identity/caps path.

Useful features include:

- `urn:xmpp:jingle-message:0`
- `urn:xmpp:jingle:1`
- `urn:xmpp:jingle:apps:rtp:1`
- `urn:xmpp:jingle:apps:rtp:audio`
- `urn:xmpp:jingle:transports:ice-udp:1`
- `urn:xmpp:jingle:apps:dtls:0`

Cheogram checks active devices and RTP capability. If the bridge does not advertise capabilities, some peers may not call it or may route calls to other devices only.

This may require changes in `patched-xmpp` or connector disco handling.

## TURN And NAT

The bridge should not run a media proxy initially, but calls will still need working ICE.

Important questions:

- Do Matrix clients include usable ICE servers in their SDP/candidate flow?
- Does Cheogram/JMP discover STUN/TURN services over XMPP external services?
- Can both sides use their own ICE servers, or does one side need the other's TURN credentials?
- Are Matrix client relay candidates accepted by Cheogram after translation?

Cheogram has external service discovery under `im.conversations.android.xmpp.model.disco.external.Services` and uses WebRTC ICE server configuration. A bridge implementation may need to discover XMPP external services and expose equivalent ICE servers to Matrix, but that is not guaranteed to be possible through legacy Matrix call events.

If direct ICE fails often, media proxying or a dedicated WebRTC gateway may become necessary. Treat that as a separate project.

## Security Notes

- Validate all incoming Jingle and SDP strictly.
- Do not accept file-transfer or unknown Jingle descriptions in the call path.
- Do not bridge video accidentally.
- Enforce sane limits on SDP size, candidate count, and call lifetime.
- Avoid logging full SDP or ICE candidates at info level; they may contain local network details.
- Preserve TLS/XMPP auth hardening in `patched-xmpp`; call work should not weaken connection security.
- Be careful with phone numbers in logs. JMP contacts are often phone-number JIDs.

## Suggested Milestones

### Milestone 1: Matrix Call Event Spike

Goal: prove a Matrix client will display a call UI from bridge-sent events.

Tasks:

- Send a synthetic `m.call.invite` into a test portal room.
- Confirm Element or the target Matrix client displays an incoming audio call.
- Confirm the bridge can receive `m.call.answer`, `m.call.candidates`, and `m.call.hangup` from that client.
- Document which Matrix clients work.

No XMPP media work should be done before this is proven.

### Milestone 2: Jingle IQ Parsing And Tests

Goal: parse and emit Jingle RTP audio IQs without connecting media.

Tasks:

- Add Jingle IQ structs.
- Add audio RTP description structs.
- Add ICE-UDP transport structs.
- Add DTLS fingerprint structs.
- Add reason/error structs.
- Round-trip sample XML from Cheogram if available.
- Add tests for unsupported video/file-transfer payloads.

### Milestone 3: SDP/Jingle Conversion Tests

Goal: convert real Matrix SDP to Jingle and back.

Tasks:

- Capture sample audio-only Matrix offer/answer SDP from target clients.
- Capture sample Cheogram session-initiate/session-accept XML.
- Build parser/renderer tests around those fixtures.
- Support Opus first.
- Preserve ICE credentials, DTLS fingerprints, payload IDs, and candidate semantics.

### Milestone 4: Outgoing Matrix To Cheogram Audio

Goal: Matrix user can place an audio call to a JMP/Cheogram contact.

Recommended first real call direction because Matrix starts with SDP.

Tasks:

- Matrix `m.call.invite` -> XMPP `propose`.
- XMPP `proceed` -> XMPP `session-initiate` with converted Matrix offer.
- XMPP `session-accept` -> Matrix `m.call.answer`.
- Candidate translation both ways.
- Hangup translation both ways.
- Busy/reject handling.

### Milestone 5: Incoming Cheogram To Matrix Audio

Goal: JMP/Cheogram incoming call can be answered in Matrix.

Tasks:

- XMPP `propose` -> pending bridge call state.
- Decide whether to auto-send `proceed` to obtain `session-initiate`.
- XMPP `session-initiate` -> Matrix `m.call.invite`.
- Matrix `m.call.answer` -> XMPP `session-accept`.
- Candidate translation both ways.
- Matrix decline/hangup -> XMPP `reject`, `retract`, or `session-terminate` depending on state.

### Milestone 6: Reliability

Goal: make it phone-replacement reliable.

Tasks:

- Timeouts.
- Duplicate candidate suppression.
- Reconnect handling.
- Multi-device tie-break behavior.
- Busy behavior.
- Better call history notices.
- Better diagnostics through the management room `ping` command or a future `calls` command.

## Testing Strategy

Unit tests:

- Existing call notice tests in `connector/xmpp_call_test.go` should continue to pass.
- Add XML parse/render tests for every Jingle action.
- Add SDP parse/render tests using real fixtures.
- Add state-machine tests for normal, decline, busy, timeout, duplicate, and out-of-order flows.
- Add Matrix event conversion tests for `m.call.*` payloads.

Integration tests:

- Cheogram Android to Matrix client, incoming audio.
- Matrix client to Cheogram Android, outgoing audio.
- Cheogram Android to bridge while another call is active, expect busy.
- Matrix hangup before XMPP `proceed`.
- XMPP retract before Matrix answer.
- ICE candidates arriving before answer/session-accept.
- Bridge reconnect during ringing.

Manual test matrix:

- At least one Element client version.
- Any other Matrix client expected to be used for calls.
- Cheogram Android current release.
- JMP-backed phone-number contact.
- Same-account multi-device scenario if possible.

## First Code Spike Recommendation

Do not start by porting all of Cheogram's Jingle stack.

Start with this sequence:

1. Prove Matrix call event send/receive in a portal room.
2. Add Jingle IQ XML structs with tests.
3. Add audio-only SDP/Jingle conversion with fixtures.
4. Implement outgoing Matrix -> Cheogram audio calls first.
5. Implement incoming Cheogram -> Matrix audio calls after the outgoing media path works.

The reason to prefer outgoing first is that Matrix already gives the bridge a complete SDP offer in `m.call.invite`. Incoming Cheogram calls begin with a JMI `propose` that does not yet contain the full SDP-equivalent Jingle content, so the bridge must make a policy decision about when to send `proceed`.

## Open Questions

- Which Matrix clients must work?
- Do those clients still support legacy 1:1 Matrix VoIP events in bridged rooms?
- Can bridgev2 send arbitrary room events cleanly through the portal abstraction, or should the connector use the Matrix intent directly?
- Does JMP/Cheogram require any capability advertisement before sending calls to this bridge device?
- Does the bridge need to discover XMPP external services for TURN/STUN?
- Is auto-sending XMPP `proceed` on incoming calls acceptable, or does it create bad UX on the Cheogram side?
- How should missed calls be represented when Matrix clients do not support call events?

Answer these with small experiments before committing to the full implementation.
