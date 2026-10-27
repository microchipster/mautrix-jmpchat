# Rust Cheogram Audio Call Client Spec

This document describes a standalone Linux client for Cheogram/JMP audio calls, implemented in Rust. It is not a Matrix bridge and it is not a Matrix client. It is a Cheogram-compatible XMPP/Jingle endpoint focused on phone-replacement audio calling.

The motivating use case is simple: log into a JMP/Cheogram XMPP account on Linux, receive incoming phone calls, place outgoing calls, and route audio through the normal Linux desktop audio stack.

## Product Scope

Build a Linux-only Cheogram/JMP audio call client.

In scope:

- XMPP login to a Cheogram/JMP account.
- Persistent XMPP connection with reconnect and stream management where feasible.
- Incoming audio call notifications.
- Outgoing audio calls to JIDs such as `+15551234567@cheogram.com`.
- Answer, decline, hang up, busy, timeout, and missed-call handling.
- Audio-only Jingle RTP sessions compatible with Cheogram Android.
- Linux audio through PipeWire, either directly or through a media framework that uses PipeWire underneath.
- CLI-first control interface.
- Optional TUI after the call path works.
- Optional desktop notifications after the daemon is stable.

Out of scope for the first implementation:

- Matrix support.
- Video calls.
- Group calls.
- SIP/PSTN gateway logic beyond what JMP/Cheogram already expose over XMPP.
- Chat messaging.
- File transfer.
- OMEMO messaging.
- Call recording.
- Media proxying or conferencing.
- Mobile support.
- Windows/macOS support.

The application should behave like a small Linux phone endpoint, not like a full chat client.

## Non-Goals

Do not implement a full Cheogram clone. Cheogram Android remains the source of truth for protocol behavior, but this app should only implement the subset required for audio calls.

Do not implement a Matrix/XMPP call bridge in this project. A standalone endpoint has much cleaner semantics because it owns the media session instead of translating between two independent clients.

Do not start with a GUI. A GUI can be added later, but it should not block getting the protocol and media path correct.

Do not assume PipeWire alone is a complete WebRTC implementation. PipeWire is the Linux audio/video graph and device/session layer. It does not by itself provide ICE, STUN/TURN, DTLS-SRTP, RTP packetization, Opus negotiation, jitter buffering, or Jingle signaling.

## Cheogram Compatibility Target

Cheogram Android is the reference implementation. Important files in this repository checkout:

- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/JingleConnectionManager.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/JingleRtpConnection.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/RtpContentMap.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/SessionDescription.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/stanzas/RtpDescription.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/stanzas/IceUdpTransportInfo.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/stanzas/Content.java`
- `cheogram-android/src/main/java/eu/siacs/conversations/xmpp/jingle/stanzas/Reason.java`

The Rust client should mimic Cheogram's call behavior closely enough that JMP/Cheogram servers and other Cheogram clients see it as another normal RTP-capable XMPP resource.

Protocol behavior to match:

- Use XEP-0353 Jingle Message Initiation for call proposal/ringing/accept/reject/finish signaling.
- Use XEP-0166 Jingle IQs for the actual RTP session.
- Use XEP-0167 Jingle RTP descriptions.
- Use XEP-0176 ICE-UDP transport.
- Use XEP-0320 DTLS-SRTP fingerprints.
- Prefer audio-only Opus first.
- Support one active call at a time initially.
- Send busy/decline/timeout behavior that matches Cheogram's expectations.
- Handle multi-device and race/tie-break cases conservatively.

## User Experience

The first product should be daemon plus CLI.

Example commands:

```sh
cheogram-call login user@cheogram.com
cheogram-call daemon
cheogram-call status
cheogram-call call +15551234567@cheogram.com
cheogram-call answer
cheogram-call decline
cheogram-call hangup
cheogram-call calls
```

Suggested UX behavior:

- `cheogram-call login` prompts for password and stores credentials in the desktop secret service if available.
- `cheogram-call daemon` starts the long-running XMPP/media process.
- Incoming call prints to the daemon log and emits a desktop notification if notification support is enabled.
- The CLI can answer/decline/hangup by talking to the daemon over a local IPC socket.
- If no TUI is running, incoming calls still ring audibly or through notifications.
- Only one active call is supported initially; new incoming calls while busy should be rejected as busy.
- A later TUI can show current account, connection state, incoming call, active call duration, mute state, and recent missed calls.

## Process Architecture

Recommended binaries:

- `cheogram-call-daemon`: owns XMPP connection, call state, media engine, audio devices, and local IPC.
- `cheogram-call`: CLI control client.
- `cheogram-call-tui`: optional interactive terminal UI.

The daemon is the only process that should hold XMPP credentials and own active media sessions.

Suggested crate/workspace layout:

```text
cheogram-call/
  Cargo.toml
  crates/
    cheogram-core/
      src/account.rs
      src/config.rs
      src/error.rs
      src/jid.rs
    cheogram-xmpp/
      src/connection.rs
      src/sasl.rs
      src/disco.rs
      src/jingle_message.rs
      src/jingle_iq.rs
      src/jingle_rtp.rs
    cheogram-media/
      src/engine.rs
      src/sdp.rs
      src/ice.rs
      src/audio.rs
      src/pipewire.rs
    cheogram-daemon/
      src/main.rs
      src/call_manager.rs
      src/ipc.rs
      src/notifications.rs
    cheogram-cli/
      src/main.rs
    cheogram-tui/
      src/main.rs
```

Keep protocol parsing and media control separate. The Jingle state machine should be testable without opening audio devices.

## Rust Technology Choices

Likely useful crates:

- `tokio` for async runtime.
- `tracing` and `tracing-subscriber` for logs.
- `clap` for CLI parsing.
- `serde`, `serde_json`, and `toml` for config and IPC payloads.
- `thiserror` or `anyhow` for error handling. Prefer typed errors in core crates.
- `jid` for JID parsing if it is sufficient.
- `xmpp-parsers` for stanza models if it covers enough Jingle; otherwise use it selectively.
- `quick-xml` or `minidom` if hand-rolled XML structs are simpler.
- `zbus` for D-Bus/desktop portal integration if needed.
- `notify-rust` for desktop notifications if acceptable.
- `keyring` for secret storage.
- `ratatui` and `crossterm` for an optional TUI.

Media stack candidates are discussed separately below because this is the most important technical decision.

## PipeWire And Media Stack Decision

PipeWire should be the default Linux audio integration layer, but PipeWire is not a replacement for a WebRTC stack.

PipeWire provides:

- Microphone capture.
- Speaker playback.
- Device routing.
- Desktop audio policy integration.
- Screen/camera capture if video is ever added.
- Low-latency graph scheduling.

PipeWire does not provide the full call media stack:

- It does not perform XMPP Jingle signaling.
- It does not perform ICE connectivity checks.
- It does not talk STUN/TURN by itself.
- It does not negotiate DTLS-SRTP with the remote endpoint by itself.
- It does not convert Jingle RTP descriptions to SDP.
- It does not implement WebRTC PeerConnection semantics by itself.
- It does not solve jitter buffering, packet loss concealment, echo cancellation, or Opus RTP packetization as a complete endpoint API.

Therefore the application needs both:

- PipeWire or PipeWire-compatible audio input/output.
- A WebRTC/RTP media engine.

### Option A: GStreamer `webrtcbin` With PipeWire Elements

This is the most pragmatic Rust/Linux route.

Use GStreamer for:

- WebRTC peer connection behavior.
- ICE/STUN/TURN integration.
- DTLS-SRTP.
- RTP/RTCP.
- Opus encode/decode/pay/depay.
- Jitter buffering.
- Audio pipeline construction.

Use PipeWire through GStreamer elements:

- `pipewiresrc` or PulseAudio compatibility source for capture.
- `pipewiresink` or PulseAudio compatibility sink for playback.

Pros:

- Mature Linux media stack.
- Good PipeWire integration.
- Rust bindings exist.
- Avoids implementing low-level RTP/DTLS/ICE details manually.
- Easier future video support if needed.

Cons:

- Native dependencies and packaging complexity.
- GStreamer API has a learning curve.
- Debugging pipelines can be its own discipline.
- Not a pure Rust media engine.

If the goal is a working Linux phone endpoint, this is likely the safest path.

### Option B: `webrtc-rs` Plus PipeWire Audio

Use a Rust WebRTC stack such as the `webrtc` crate for ICE/DTLS/SRTP/RTP and use PipeWire bindings or another Linux audio crate for capture/playback.

Pros:

- More Rust-native.
- Potentially less dynamic pipeline machinery.
- Better control over signaling integration.

Cons:

- More interop risk with Cheogram/libwebrtc endpoints.
- Audio capture/playback integration is still a separate problem.
- Echo cancellation and audio processing are harder.
- More protocol/media details fall on this project.

This may be viable, but it should be proven with a small call prototype before committing.

### Option C: libwebrtc Bindings Plus PipeWire

Use Google's libwebrtc through Rust bindings or a C++ shim.

Pros:

- Maximum compatibility with WebRTC endpoints.
- Mature ICE/DTLS/SRTP/audio processing.

Cons:

- Build and packaging complexity.
- C++ integration.
- Harder cross-distro distribution.
- Not a pleasant dependency for a small CLI app.

This is probably only worth it if GStreamer or `webrtc-rs` cannot interoperate reliably.

### Option D: Raw PipeWire Plus Manual RTP/ICE/DTLS

Do not start here.

This would mean implementing or assembling:

- ICE agent.
- STUN/TURN.
- DTLS handshake.
- SRTP key extraction.
- RTP packetization/depacketization.
- RTCP.
- Opus encode/decode.
- jitter buffering.
- packet loss handling.
- echo cancellation.

PipeWire would only handle local audio graph nodes. Everything else would still need to be built or integrated. This is a research project, not the practical MVP.

### Recommendation

Start with GStreamer `webrtcbin` using PipeWire-backed audio sources/sinks. Keep the media engine behind a Rust trait so a future `webrtc-rs` backend can be tested without rewriting XMPP/Jingle logic.

Suggested abstraction:

```rust
#[async_trait::async_trait]
pub trait MediaEngine {
    async fn create_offer(&mut self, media: MediaSpec) -> Result<SdpOffer>;
    async fn accept_offer(&mut self, offer: SdpOffer) -> Result<SdpAnswer>;
    async fn set_remote_answer(&mut self, answer: SdpAnswer) -> Result<()>;
    async fn add_remote_candidate(&mut self, candidate: IceCandidate) -> Result<()>;
    async fn close(&mut self) -> Result<()>;
    fn events(&mut self) -> MediaEventStream;
}
```

Do not let GStreamer types leak into the call state machine. Convert at the media boundary.

## XMPP Connection

The client needs a robust XMPP session layer.

Required features:

- TCP/TLS XMPP client connection.
- SCRAM-SHA-512, SCRAM-SHA-256, SCRAM-SHA-1.
- SCRAM-PLUS support if practical, or at least compatibility with servers that advertise it.
- XEP-0198 stream management if available.
- Reconnect with exponential backoff.
- Presence handling.
- Disco feature discovery.
- Message handling for XEP-0353.
- IQ routing for Jingle.

If Rust XMPP libraries do not support all of this cleanly, implement missing pieces directly rather than pulling the app away from Cheogram-compatible behavior.

The `go-xmpp` fork used by the bridge has useful behavior to reference, especially SCRAM hardening and stream management fixes, but this Rust app should not depend on Go code.

## XMPP Features To Advertise

The client should eventually advertise call capability through service discovery/caps.

Likely features:

- `urn:xmpp:jingle-message:0`
- `urn:xmpp:jingle:1`
- `urn:xmpp:jingle:apps:rtp:1`
- `urn:xmpp:jingle:apps:rtp:audio`
- `urn:xmpp:jingle:transports:ice-udp:1`
- `urn:xmpp:jingle:apps:dtls:0`

Cheogram makes routing decisions based on active RTP-capable devices. If this client does not advertise capability correctly, incoming calls may not be routed to it.

## Jingle Message Initiation

Implement these message payloads:

- `propose`
- `ringing`
- `proceed`
- `reject`
- `retract`
- `finish`

Required behavior:

- Incoming `propose` with audio RTP description creates a pending incoming call.
- Incoming `propose` with unsupported media should be ignored or rejected cleanly.
- Incoming `ringing` updates outgoing call UI state.
- Incoming `proceed` allows an outgoing caller to send Jingle `session-initiate`.
- Incoming `reject` terminates pending outgoing proposal.
- Incoming `retract` cancels pending incoming proposal.
- Incoming `finish` marks established or historical call complete.

Outgoing behavior:

- Outgoing call starts with `propose` to the peer bare JID.
- Answering incoming call sends `proceed`.
- Declining incoming call sends `reject` before the Jingle session exists.
- Cancelling outgoing call sends `retract` before the Jingle session exists.
- Ending established call sends `session-terminate` and `finish` where Cheogram would.

Use Cheogram's generated XML as the compatibility reference.

## Jingle RTP IQs

Implement these Jingle IQ actions:

- `session-initiate`
- `session-accept`
- `transport-info`
- `session-terminate`

Optional later:

- `content-add`
- `content-accept`
- `content-reject`
- `content-remove`
- `content-modify`

For audio-only MVP, content modification can be rejected or ignored with a clear reason.

Jingle model structs should represent:

- Session ID.
- Action.
- Initiator/responder JID if present.
- Content creator.
- Content name, likely `audio`.
- RTP media description.
- Payload types.
- RTP header extensions if needed.
- RTCP feedback if needed.
- ICE credentials.
- ICE candidates.
- DTLS fingerprint.
- Reason and reason text.

All Jingle IQs must receive proper IQ responses. Do not forget to ack valid incoming IQs. Jingle peers expect IQ request/response semantics even when the action also changes call state.

## Audio Codec Scope

Start with Opus.

Required:

- Opus at 48000 Hz.
- Common payload type negotiation from remote SDP/Jingle.
- Mono and stereo handling, but mono output is acceptable if negotiated correctly.
- DTMF is optional for MVP, but phone replacement may eventually need it.

Optional later:

- Telephone-event DTMF.
- PCMU/PCMA fallback if JMP ever needs PSTN-like codec fallback.
- Codec preference configuration.

## SDP And Jingle Conversion

Even with GStreamer, the app must translate between Jingle XML and the media engine's SDP-oriented interface.

The conversion layer should be explicit and heavily tested.

From Jingle to SDP:

- Convert RTP payload types to `m=audio` payload IDs and `a=rtpmap` lines.
- Convert FMTP parameters to `a=fmtp`.
- Convert RTCP feedback to `a=rtcp-fb`.
- Convert ICE `ufrag` and `pwd` to `a=ice-ufrag` and `a=ice-pwd`.
- Convert DTLS fingerprint to `a=fingerprint`.
- Convert DTLS setup to `a=setup`.
- Convert candidates to `a=candidate` lines or trickle candidates.
- Preserve content name as SDP `mid`.

From SDP to Jingle:

- Parse only one audio m-line for MVP.
- Reject video m-lines.
- Extract payload types, codecs, FMTP, RTCP feedback.
- Extract ICE credentials.
- Extract DTLS fingerprint and setup role.
- Extract candidates.
- Generate Jingle content with ICE-UDP transport.

Do not build this with ad hoc string concatenation everywhere. Create a small typed intermediate model and parse/render from both sides.

Suggested types:

```rust
pub struct AudioSessionDescription {
    pub mid: String,
    pub direction: MediaDirection,
    pub codecs: Vec<RtpCodec>,
    pub ice: IceCredentials,
    pub fingerprint: DtlsFingerprint,
    pub setup: DtlsSetup,
    pub candidates: Vec<IceCandidate>,
}
```

## Call State Machine

Use an explicit state machine. Do not spread call state across unrelated async callbacks.

Suggested states:

- `Idle`
- `IncomingProposed`
- `OutgoingProposed`
- `Ringing`
- `ProceedSent`
- `ProceedReceived`
- `SessionInitiated`
- `SessionAccepted`
- `ConnectingMedia`
- `Connected`
- `Terminating`
- `Terminated`
- `Failed`

Suggested events:

- `UserCallRequested`
- `UserAnswerRequested`
- `UserDeclineRequested`
- `UserHangupRequested`
- `XmppProposeReceived`
- `XmppProceedReceived`
- `XmppRejectReceived`
- `XmppRetractReceived`
- `XmppFinishReceived`
- `XmppSessionInitiateReceived`
- `XmppSessionAcceptReceived`
- `XmppTransportInfoReceived`
- `XmppSessionTerminateReceived`
- `MediaLocalCandidate`
- `MediaConnected`
- `MediaFailed`
- `Timeout`
- `Disconnected`

State machine rules:

- Only one active call at a time initially.
- Incoming call while busy gets rejected as busy.
- Outgoing call while busy returns an error to CLI/TUI.
- Duplicate Jingle messages should be idempotent where possible.
- Candidate events before session description should be buffered.
- Candidate events after terminal state should be ignored.
- XMPP reconnect during active media should terminate the call unless a safe rebound mechanism is implemented.
- Daemon shutdown should terminate active calls cleanly.

## Incoming Call Flow

Incoming Cheogram/JMP call:

1. XMPP receives JMI `propose` from peer.
2. Validate peer, session ID, and RTP audio description.
3. If busy, send busy/reject using Cheogram-compatible behavior.
4. Create call session in `IncomingProposed`.
5. Notify CLI/TUI/desktop.
6. User answers.
7. Send JMI `proceed`.
8. Receive Jingle `session-initiate` IQ.
9. Ack IQ.
10. Convert Jingle RTP content to SDP offer.
11. Ask media engine to accept offer.
12. Media engine creates SDP answer.
13. Convert SDP answer to Jingle RTP content.
14. Send Jingle `session-accept` IQ.
15. Exchange `transport-info` candidates.
16. Media reaches connected state.
17. On hangup, send `session-terminate` and JMI `finish` as appropriate.

Important: Cheogram starts the full Jingle session only after `proceed`. Do not expect `propose` to contain enough information to start media.

## Outgoing Call Flow

Outgoing Cheogram/JMP call:

1. User runs `cheogram-call call +15551234567@cheogram.com`.
2. Validate connected XMPP state and no active call.
3. Create session ID.
4. Send JMI `propose` with audio RTP description.
5. Enter `OutgoingProposed`.
6. Receive `ringing`, update UI.
7. Receive `proceed`.
8. Ask media engine to create SDP offer.
9. Convert SDP offer to Jingle RTP content.
10. Send Jingle `session-initiate` IQ.
11. Receive `session-accept` IQ.
12. Convert Jingle RTP content to SDP answer.
13. Set remote answer in media engine.
14. Exchange candidates via `transport-info`.
15. Media reaches connected state.
16. On hangup, send `session-terminate` and JMI `finish` as appropriate.

## IPC API

Use a Unix domain socket for the first implementation. D-Bus can be added later if useful.

Suggested commands:

- `status`
- `accounts`
- `login`
- `logout`
- `call {jid}`
- `answer {call_id}`
- `decline {call_id}`
- `hangup {call_id}`
- `mute {call_id}`
- `unmute {call_id}`
- `calls`
- `devices`
- `set-input {device}`
- `set-output {device}`

Suggested daemon events:

- `connection_state_changed`
- `incoming_call`
- `outgoing_call_state_changed`
- `call_connected`
- `call_ended`
- `call_failed`
- `audio_devices_changed`

Keep IPC payloads JSON initially. It is easier to debug and stable enough for a local tool.

## Configuration And Secrets

Configuration path:

- `$XDG_CONFIG_HOME/cheogram-call/config.toml`

State path:

- `$XDG_STATE_HOME/cheogram-call/state.json` or SQLite later.

Cache path:

- `$XDG_CACHE_HOME/cheogram-call/`

Secrets:

- Prefer Secret Service/keyring for password storage.
- If unavailable, allow environment variable or prompt-only operation.
- Do not store passwords plaintext by default.

Suggested config:

```toml
[account]
jid = "user@cheogram.com"

[audio]
input = "default"
output = "default"
ringtone = true

[network]
connect_timeout_seconds = 30
reconnect = true

[calls]
auto_reject_when_busy = true
incoming_timeout_seconds = 60
outgoing_timeout_seconds = 60
```

## Notifications And Ringing

Incoming calls need attention even if no terminal is focused.

MVP options:

- Print to daemon logs.
- Trigger terminal bell in TUI.
- Play a ringtone through the selected output device.
- Send desktop notification through `notify-rust`.

Later options:

- MPRIS-style controls are probably not appropriate.
- D-Bus service for desktop shell integration.
- Systemd user service unit.

Ringtone playback should use the same media/audio abstraction as call playback if possible, but it must not block accepting the call.

## Reliability Requirements

For phone replacement, the boring reliability work matters more than UI polish.

Required before daily use:

- Automatic reconnect.
- Clear status command.
- Missed call record.
- Busy rejection.
- Incoming call timeout.
- Outgoing call timeout.
- Audio device error reporting.
- ICE failure reporting.
- Graceful daemon shutdown.
- No panic on malformed XMPP stanzas.
- No leaked media sessions after hangup.

Recommended status output:

```text
Account: user@cheogram.com
XMPP: connected as user@cheogram.com/linux
Calls: idle
Audio input: default
Audio output: default
Last call: missed incoming from +15551234567 at 2026-04-30 12:34
```

## Testing Plan

Unit tests:

- JID handling.
- XMPP JMI parse/render.
- Jingle IQ parse/render.
- SDP parse/render.
- Jingle-to-SDP conversion.
- SDP-to-Jingle conversion.
- State machine transitions.
- Busy/timeout/duplicate handling.
- IPC command parsing.

Fixture tests:

- Capture Cheogram `propose`, `proceed`, `session-initiate`, `session-accept`, `transport-info`, and `session-terminate` XML.
- Capture SDP generated by the selected media engine.
- Round-trip all fixtures through typed models.

Integration tests:

- Connect to a test XMPP account.
- Outgoing call makes Cheogram Android ring.
- Incoming Cheogram call makes CLI/TUI ring.
- Decline incoming call from CLI.
- Hang up outgoing call before answer.
- Complete one incoming audio call.
- Complete one outgoing audio call.
- Reject second incoming call while busy.

Manual tests:

- JMP phone-number contact.
- Cheogram Android current release.
- Different NAT networks.
- Headphones and speaker output.
- Microphone mute/unmute.
- Suspend/resume laptop.
- Network loss during ringing.
- Network loss during active call.

## Implementation Milestones

### Milestone 1: XMPP Login And Status

- Implement config and secret storage.
- Connect to XMPP.
- Authenticate with SCRAM.
- Send initial presence.
- Print connection status.
- Reconnect after network failure.

### Milestone 2: JMI Ringing Without Media

- Parse incoming `propose`.
- Notify CLI/TUI.
- Send `reject` on decline.
- Send `propose` for outgoing call.
- Receive `ringing`, `reject`, `retract`, and `finish`.
- Track missed calls.

This proves Cheogram-compatible call discovery before the media stack is involved.

### Milestone 3: Jingle IQ And SDP Fixtures

- Implement Jingle RTP structs.
- Implement ICE-UDP transport structs.
- Implement DTLS fingerprint structs.
- Add fixture round-trip tests.
- Implement typed audio session model.
- Implement SDP conversion tests.

### Milestone 4: Media Engine Spike

- Build a local GStreamer/PipeWire audio loopback or WebRTC test.
- Confirm microphone capture and speaker playback.
- Confirm Opus encode/decode.
- Confirm ICE candidate generation.
- Confirm SDP offer/answer generation.

If using `webrtc-rs`, do the equivalent with local audio capture/playback and a test peer.

### Milestone 5: Incoming Audio Call

- Receive `propose`.
- Answer from CLI.
- Send `proceed`.
- Receive `session-initiate`.
- Start media engine from remote offer.
- Send `session-accept`.
- Exchange candidates.
- Complete audio call.
- Hang up cleanly.

### Milestone 6: Outgoing Audio Call

- Send `propose`.
- Receive `proceed`.
- Create local offer.
- Send `session-initiate`.
- Receive `session-accept`.
- Exchange candidates.
- Complete audio call.
- Hang up cleanly.

### Milestone 7: Daily Driver Hardening

- Systemd user service.
- Desktop notifications.
- TUI.
- Device selection.
- Reconnect behavior.
- Better logs.
- Packaging.
- Crash recovery.

## Packaging

Linux-only packaging options:

- Distro package later.
- AppImage is possible but GStreamer dependencies can be awkward.
- Flatpak may fit PipeWire and desktop portals well.
- Nix flake can be useful for development reproducibility.

If using GStreamer, packaging must include or depend on the right plugins:

- core GStreamer runtime.
- WebRTC plugin.
- RTP/RTCP plugins.
- Opus plugin.
- nice/libnice ICE plugin.
- PipeWire plugin.

If using `webrtc-rs`, packaging shifts from GStreamer plugins to audio backend libraries and possibly Opus libraries.

## Risks

Highest risks:

- Media stack choice proves unreliable or hard to package.
- Cheogram/Jingle SDP conversion has subtle incompatibilities.
- XMPP capability advertisement is incomplete, so calls do not route to the Linux client.
- NAT/TURN behavior fails outside local networks.
- Echo cancellation/audio processing is poor enough to make calls unpleasant.
- Rust XMPP libraries require substantial missing protocol work.

Mitigations:

- Build XML and SDP fixture tests early.
- Capture real Cheogram traffic in a controlled test account.
- Keep media backend behind a trait.
- Start with Opus-only audio.
- Start with one active call at a time.
- Defer GUI until the media path is proven.

## Open Questions

- Which Rust XMPP stack is closest to supporting modern client auth, stream management, disco, and custom Jingle IQ routing?
- Does Cheogram/JMP require entity capabilities before routing calls to this client resource?
- Which media backend should be selected after a one-week prototype: GStreamer `webrtcbin`, `webrtc-rs`, or libwebrtc bindings?
- Can the app rely on PipeWire's PulseAudio compatibility for the first audio backend, or should it use PipeWire-native APIs from day one?
- How should TURN/STUN servers be discovered and configured?
- Is DTMF required for the first phone-replacement version?
- What is the minimum acceptable CLI/TUI UX for answering calls quickly?

## Recommendation

The most practical plan is:

1. Build the daemon, CLI, XMPP login, and JMI call ringing first.
2. Prototype GStreamer `webrtcbin` with PipeWire-backed audio.
3. Keep the media engine abstract so `webrtc-rs` can be evaluated in parallel or later.
4. Implement incoming Cheogram audio calls before outgoing media calls.
5. Add TUI and desktop integration only after a full audio call works.

PipeWire should absolutely be the Linux audio target. The important distinction is that PipeWire is the local audio system, while the application still needs a WebRTC/RTP engine to speak to Cheogram over the network.
