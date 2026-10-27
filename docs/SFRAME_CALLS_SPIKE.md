<!--
SPDX-FileCopyrightText: 2026 Slavi Pantaleev

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Spike: SFrame media E2EE for calls in encrypted rooms

Status: proposal, not started. Interim state (call rooms) stays deployable and is being polished in parallel.

Goal: let MatrixRTC (Element Call) calls with a bridge participant work inside **encrypted** portal rooms, instead of ringing in a separate unencrypted "call room". Audio only — video is out of scope for JMPChat.

## Why this is needed

Element Call uses MatrixRTC (MSC4143) over a LiveKit SFU. In encrypted rooms it turns on media E2EE, which is **SFrame** (RFC 9605) applied to the encoded media frames, with keys negotiated through Matrix itself. A bridge participant that cannot speak SFrame would hear noise (inbound frames are encrypted) and would be inaudible (its outbound frames would be plaintext, or rejected by clients enforcing E2EE). That is why the bridge currently refuses to ring in encrypted rooms and points at a dedicated unencrypted call room (`call-room`, `calls.auto_create_call_room`).

We already maintain forks (e.g. the bridge itself), so maintaining a fork of `mrtc-sip-bridge` is acceptable.

## How Element Call E2EE works (to be confirmed in Phase 0)

- A call is a MatrixRTC session: participants publish `m.call.member` state events containing the LiveKit foci (SFU URLs) they use.
- Media goes through the LiveKit SFU, which forwards encrypted frames without being able to decrypt them (E2EE is end-to-end between participants).
- Each participant generates a key and shares it with the other participants **via Matrix to-device messages** (element-call / matrix-js-sdk `webrtc` module). The key is used to derive per-sender SFrame encryption state (key ID + ratchet), and each encrypted media frame is tagged with the sender's key ID.
- Keys rotate when participants join/leave.
- On the client side LiveKit's E2EE implementation is used (a `KeyProvider` interface + an SFrame codec, in JS and Rust).

Phase 0 must pin down exact event names, signature/trust rules, and how a non-device participant (appservice ghost) can receive keys, because that is the riskiest part of the whole spike.

## Candidate bridge participation models

1. **Appservice device keys (MSC3202-style)** — the bridge acts as a device for the ghost (or for the double-puppeted user) and receives to-device key events. goolm crypto store already exists in our build; mautrix tooling has appservice device support.
2. **Bridge device on a separate Matrix account** — simpler crypto-wise, but then it is not a member of the user's room and would not receive keys; rejected unless combined with an appservice user.
3. **User's double-puppet device** — the AS cannot sign/own device keys, so keys would not be addressed to it; likely unusable for E2EE.

Spike M1/M2 decide between these.

## Workstreams

### WS-A: SFrame codec in Go (or a cgo bridge)

Deliverable: a small Go package (`sframe/`) implementing:

- SFrame header parsing (KID, CTR, encryption scheme: AES-GCM and AES-CTR + HMAC per RFC 9605).
- Key derivation (HKDF) and ratchet advancement matching the LiveKit/Element Call implementation exactly.
- Test vectors: at minimum, frames produced by `livekit-client`'s E2EE worker; ideally cross-test against the Rust implementation.

Options: implement from the RFC (~1–2k LOC + tests), or cgo-wrap an existing Rust/C++ implementation. The cgo path makes the image heavier and the fork harder; prefer a pure Go implementation unless interop proves impossible.

### WS-B: Key distribution and device identity

Deliverable: the bridge receives, stores and rotates per-participant SFrame keys.

- Bridge needs a Matrix device identity that other clients will address. Investigate MSC3202 appservice device support in `mautrix-go` and whether Element Call sends keys to unverified devices (it appears to fan out to all devices of the participants; must be confirmed).
- Subscribe to to-device events (the same event the clients use), decrypt them if needed (Olm, goolm), and maintain a key table per room/call/sender.
- Handle joins/leaves and key rotations; prune on call end.

### WS-C: Media pipeline integration (fork of mrtc-sip-bridge)

Deliverable: encrypted audio round-trips through the bridge.

- Decrypt inbound LiveKit audio frames with the sender's key before decoding and forwarding to SIP.
- Encrypt outbound audio with our own key after encoding, so E2EE clients can hear the phone side.
- Wire the key table (WS-B) into the media path; keep video untouched.
- Keep changes modular and upstreamable; maintenance via our fork is acceptable.

### WS-D: Bridge integration and UX

- Replace the "encrypted portal → refuse, use call room" branch with: use SFrame when available, otherwise fall back to call rooms (config: `calls.e2ee: auto|off`, default auto).
- Keep `call-room`/`auto_create_call_room` as the fallback path and keep the interim polish (auto-create on by default, notices mirrored into the portal, call rooms hidden from listings, `📞` names).
- Status surface: `sip-status` shows whether media E2EE is active for the current call.

### WS-E: Validation

- Unit: SFrame codec vectors; key-table rotation logic.
- Integration harness: LiveKit + lk-jwt + the bridge fork + two Element Call clients in an encrypted room; verify audio both ways, key rotation on join/leave, and a client that joins mid-call.
- Interop with the Element Call versions people actually use (pin during the spike).

## Milestones (timeboxed)

- **M0 (2–3 days): research spike.** Read matrix-js-sdk `webrtc` E2EE + livekit-client E2EE + MSC4143; produce a short report pinning event names, key format, trust rules, and answer: can an appservice device receive the keys?
- **M1 (3–5 days): Go SFrame codec.** Decrypt frames generated by livekit-client; encrypt frames that livekit-client decrypts.
- **M2 (3–5 days): key receipt prototype.** Bridge device receives and tracks keys in a test room with two Element Call clients.
- **M3 (1 week): media round-trip.** Forked mrtc-sip-bridge decrypts/encrypts audio in a live call; SIP side hears and is heard.
- **M4: bridge integration + docs + tests.** Feature flag, fallback, status reporting.

If M1 or M2 fails (e.g. keys are not sent to appservice devices, or the codec cannot be matched), we stop and keep call rooms as the permanent approach — no sunk cost beyond the spike.

## Risks

- Spec/implementation churn: media E2EE is young; Element Call and LiveKit may change the framing or key handling between releases. Mitigation: pin versions, keep the fork thin, test against two client versions.
- Trust/verification: if clients only send keys to verified devices, the bridge would need verification UX; confirm in M0.
- Performance: SFrame is cheap (AES-GCM per frame), but the Go implementation must avoid allocations in the audio path.
- Maintenance: forked mrtc-sip-bridge must be rebased; keep patches isolated (new files + minimal hooks).

## Non-goals

- Video bridging.
- Group-call SFU-side recordings or transcription.
- Replacing the call-room model for gateways or clients that cannot do E2EE.

## Sequencing with current work

1. Deploy the current bridge as-is (single always-full image, calls opt-in per account).
2. Interim polish (this cycle): auto-create call rooms by default, mirror ringing/notices into the encrypted portal, hide call rooms from listings, `📞` naming.
3. Start M0/M1/M2 after deployment is validated; M3/M4 decided by spike outcome.
