<!--
SPDX-FileCopyrightText: 2026 Slavi Pantaleev

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# SFrame calls M0 findings

Result of the M0 research spike for [SFRAME_CALLS_SPIKE.md](SFRAME_CALLS_SPIKE.md). Read-only research; no code changed.

Sources are pinned to what was inspected on 2026-10-27:

- `livekit/client-sdk-js` @ `20f4a108` (main)
- `element-hq/element-call` @ `619f5ac3` (main)
- `matrix-org/matrix-js-sdk` @ `6699a308` (main)
- `matrix-org/matrix-rust-sdk` @ `cdfdcc91` (main) and `matrix-sdk-crypto-wasm` v18.9.0
- `github.com/livekit/server-sdk-go/v2` v2.18.1 (already a dependency of `mrtc-sip-bridge` v0.3.1)
- `maunium.net/go/mautrix` v0.32.0
- `element-hq/synapse` `develop` branch at time of reading
- `matrix-org/matrix-spec-proposals` @ `a6d4f687` (MSC4143, MSC4195, MSC4190, MSC4326)
- RFC 9605 (August 2024)
- `github.com/williamkray/mrtc-sip-bridge` v0.3.1 (Go module cache, read-only)

**Headline:** the codec is a solved problem in Go (the LiveKit Go SDK already implements it), but it is **not** RFC 9605 and it should not be implemented from that RFC. Key distribution is viable for an appservice device on Synapse with existing mautrix-go primitives, with two concrete gaps to build. No hard blocker was found, so M1 and M2 both look worth attempting.

## 1. Verified facts

### 1.1 RFC 9605, for orientation only

RFC 9605 defines SFrame as a variable-length header plus AEAD ciphertext, per-frame or per-packet:

- Header: a config byte with extended-KID flag `X`, 3-bit `K`/`KIDKLEN`, extended-CTR flag `Y`, 3-bit `C`/`CLEN`, followed by big-endian KID and CTR varints (RFC 9605 §4.3, figures 3 and 4).
- Key derivation: `HKDF-Extract("", base_key)` then HKDF-Expand with labels `"SFrame 1.0 Secret key " + KID(8-byte BE) + cipher_suite` and `"SFrame 1.0 Secret salt " + ...` (§4.4.2).
- Nonce: `encode_big_endian(CTR, Nn) XOR sframe_salt`; header (+ optional metadata) is AAD (§4.4.3).
- Cipher suites (§4.5, and IANA registry §8.1): `AES_128_CTR_HMAC_SHA256_{80,64,32}`, `AES_128_GCM_SHA256_128`, `AES_256_GCM_SHA512_128`. Short tags (32-bit) are explicitly dangerous (§7.5).
- Audio-only note: audio frames have no inter-frame dependencies, so key rotation does not need key-frame handling (§6.2). "Metadata moved to an RTP header extension" is a per-frame mode suggestion (§4.4.3); SFrame itself is transport-agnostic.
- Sender-key ratchet scheme in §5.1 (`base_key[i+1] = HKDF-Expand(HKDF-Extract("", base_key[i]), "SFrame 1.0 Ratchet", Nh)`) and KID = `key_generation << R | ratchet_step % 2^R`.
- RFC 9605 contains test vectors in Appendix C (header encoding, AES-CTR+HMAC AEAD, full SFrame encrypt/decrypt).

### 1.2 What LiveKit and Element Call actually use ("SFrame" in Matrix docs) — NOT RFC 9605

`livekit-client` implements a custom scheme, described in its own code as "similar to the frame header described in draft-omara-sframe-00, but we put it at the end" (`src/e2ee/worker/FrameCryptor.ts`, encode/decode comments). Audio frame layout on the wire (RTP payload):

```
[TOC byte (1, cleartext)] [AES-GCM ciphertext] [IV (12)] [IV length (1)=12] [KID (1)]
```

- AAD is the cleartext first byte (Opus TOC); the rest of the Opus packet is encrypted. Constants: `UNENCRYPTED_BYTES.audio = 1`, `IV_LENGTH = 12` (`src/e2ee/constants.ts`).
- Cipher: AES-GCM; key derivation is HKDF-SHA256 with salt `LKFrameEncryptionKey` and a 128-byte zero `info` buffer, output 128 bits (`src/e2ee/utils.ts` `deriveKeys`, `getAlgoOptions`; `src/e2ee/constants.ts` `SALT`, `KEY_PROVIDER_DEFAULTS`).
- KID is the last byte of the frame (1 byte, 0..255) and indexes a per-participant key ring. Key ring size 256 and window size 10 in Element Call's `MatrixKeyProvider` (`element-call/sdk/src/media-backend/livekit/MatrixKeyProvider.ts`); livekit defaults are 16/8.
- Ratchet: on decrypt failure the receiver derives `HKDF-Expand(material, salt=LKFrameEncryptionKey, info=128z, 32 bytes)` as new material and re-derives the AES key, up to `ratchetWindowSize` attempts (`ParticipantKeyHandler.ratchetKey`, `FrameCryptor.decryptFrame`). Senders only switch keys when a new key is set via the key provider; the per-member flow does not implicitly ratchet senders.
- The JS SDK's IV is derived from SSRC + RTP timestamp + a per-SSRC send counter (`FrameCryptor.makeIV`) — but since the IV is transmitted in the frame, an encryptor may choose any 12-byte IV (the Go SDK uses `crypto/rand`).
- Cipher suites/registry/test vectors of RFC 9605 are irrelevant to this interop path.

LiveKit's Go server SDK (pinned by our bridge fork!) ships the same format:

- `encryption.go` v2.18.1: `EncryptGCMAudioSample` / `DecryptGCMAudioSample`, `LIVEKIT_SDK_SALT = "LKFrameEncryptionKey"`, `LIVEKIT_IV_LENGTH = 12`, `LIVEKIT_KEY_SIZE_BYTES = 16`, `unencrypted_audio_bytes = 1`; `DeriveKeyFromBytes` does HKDF (salt + 128-byte zero info).
- `e2ee/keyprovider.go`: `KeyProvider` interface (`GetKey(index)`, `CurrentKeyIndex()`), `ExternalKeyProvider` (raw 16-byte AES keys).
- `e2ee/frameencryptor.go` and `e2ee/trackdecryptor.go`: `GCMFrameEncryptor` (KID = `CurrentKeyIndex()`), `GCMFrameDecryptor` (reads KID from the trailer), `TrackDecryptor` (samplebuilder + depacketizer + SIF handling).
- `localtrack.go`: `WithFrameEncryptor(enc)` encrypts each sample before RTP packetization in `WriteSample`; `room.go`: `Room.SifTrailer()`.
- `e2ee.go`: `NewFrameEncryptor(kp, lksdk.CodecOpus)` / `NewFrameDecryptor(kp, lksdk.CodecOpus, sifTrailer)`.

LiveKit's client-side key plumbing: `RoomOptions.e2ee = { keyProvider, worker }`; encryption on/off per participant; Element Call builds the provider in `sdk/src/media-backend/livekit/ConnectionFactory.ts` (`e2ee: keyProvider && {...}`) and `Publisher.ts` (`room.setE2EEEnabled`).

MSC4195 (§End-to-end encryption) codifies exactly this: LiveKit secret → custom key provider, `keySize 128`, keyring 256, `ratchetSalt = LKFrameEncryptionKey`, with an appendix noting the reference implementation uses 16-byte keys even though the proposal text says at least 32.

### 1.3 Key distribution in an encrypted room

MSC4143 (current proposal text, `proposals/4143-matrix-rtc.md`) defines `m.per_member` encryption:

- Each member generates a random sender key and distributes it in Olm-encrypted to-device messages of type `m.rtc.encryption_key` (unstable implementation name: `io.element.call.encryption_keys`, confirmed in `matrix-js-sdk/src/@types/event.ts` `EventType.CallEncryptionKeysPrefix`).
- Event content (MSC4143): `{ room_id, member_id, media_key: { index, key } }`, key is unpadded base64; `index` is 0..255 and "WebRTC-based transports may use this as the `keyID` field of SFrame headers".
- Recipients are the devices that authored joined member events — "keys are only distributed among session members. Other devices, even if in the room, never get the key material." Member events must be encrypted/authenticated; clients MUST discard cleartext key events, and SHOULD discard key events from non-cross-signed sender devices (MSC4153).
- Rotation: on join/leave, and periodically; a 5 s delay before using a new key is recommended; joins may reuse a recent key within a grace window.

matrix-js-sdk implementation (`src/matrixrtc/`):

- `RTCEncryptionManager.ts`: first key is 16 random bytes, `keyId = 0`; leavers cause a new random key + distribution to everyone; joiners get the current key (reused if newer than a 10 s grace, else rotate); `useKeyDelay` default 5 s (constructor) / 1 s via join config; key index wraps `(keyId + 1) % 256`; a key without a matching membership is buffered and retried.
- `ToDeviceKeyTransport.ts`: `sendKey` targets are all call memberships mapped to `{userId: membership.sender, deviceId: membership.deviceId}` and sent with `client.encryptAndSendToDevice(EventType.CallEncryptionKeysPrefix, ...)`. Receiving ignores events whose wire type is not `m.room.encrypted` ("sent in clear" → ignored), requires `room_id` match and `claimed_device_id`.
- `MatrixRTCSession.ts` constructs `RTCEncryptionManager` unconditionally with a `ToDeviceKeyTransport`; keys are emitted as `MatrixRTCSessionEvent.EncryptionKeyChanged(keyBin, index, membership, backendIdentity)`.
- Element Call: `sdk/src/RTCParticipation.ts` `mediaKeys$` maps that event into `{participantId: backendIdentity, index, key}` → `MatrixKeyProvider.onMediaKey` → livekit `onSetEncryptionKey(importKey(raw, "HKDF"), participantId, index)`.
- Backend identity = LiveKit identity: for `livekit_service_url` transports it is `${userId}:${deviceId}`; for MSC4195 `url` transports it is `base64(sha256(canonicalJson([userId, memberId])))` (`RTCEncryptionManager.getOwnBackendIdentities`, `membershipData/rtc.ts computeRtcIdentityRaw`).
- Element Call chooses per-participant E2EE iff the room has `m.room.encryption` (`src/e2ee/sharedKeyManagement.ts` `useRoomEncryptionSystem`; `RTCParticipation.ts` passes `encryptMedia: encryptionSystem.kind !== E2eeType.NONE`). Shared-key E2EE (`?password=`) uses livekit's `ExternalE2EEKeyProvider` instead.
- Element Call currently defaults to `MatrixRTCMode.Compatibility` (state-event memberships, `sdk/src/config.ts`, `src/settings/settings.ts`), i.e. `m.call.member`/`org.matrix.msc3401.call.member` state events, member id `${userId}:${deviceId}`. The new sticky `m.rtc.member` (MSC4354) path exists behind `Matrix_2_0` mode. matrix-js-sdk's membership code has **no** check that membership events are encrypted, so the current JS stack accepts a plaintext state-event membership even in an encrypted room.

### 1.4 Can a bridge/appservice device receive and use these keys?

Yes, in principle, and the pieces exist. The bridge must be an *Olm device that authored a joined membership*.

What a receiving appservice device needs:

1. A device (device id + device keys + published one-time keys) for the ghost user. Appservices can create devices via MSC4190 (`PUT /_matrix/client/v3/devices/{deviceId}`); Synapse implements this (`synapse/rest/client/devices.py`, `on_PUT`: "MSC4190 allows appservices to create devices"). mautrix-go wraps it: `Client.CreateDeviceMSC4190` (`client.go:1184`).
2. Device masquerading on every request: `?org.matrix.msc3202.device_id=...` (unstable) / `device_id` (stable, MSC4326). Synapse implements it in `synapse/api/auth/base.py` (the appservice access-token path accepts `device_id`; if the device does not exist it rejects with `M_UNKNOWN_DEVICE`). mautrix-go sets both query params when `SetAppServiceDeviceID` is true (`url.go`, lines 115-121).
3. An Olm machine for that device. mautrix-go has the full stack: `crypto.OlmMachine`, goolm backend, `AddAppserviceListener` which handles `event.ToDeviceEncrypted`, device lists and OTK counts arriving through appservice transactions (`crypto/machine.go`), and `EncryptToDevices` for sending encrypted to-device content back (`crypto/encryptolm.go:20`).
4. Homeserver support to deliver to-device events and the encryption metadata to the appservice: Synapse `experimental_features.msc2409_to_device_messages_enabled` (to-device in transactions, MSC4203/MSC2409) and `msc3202_transaction_extensions` (device lists / OTK counts / fallback keys). Device masquerading is enabled by default in current Synapse (see this playbook's `roles/custom/matrix-synapse/tasks/validate_config.yml`, which removed the flag as "enabled by default now"). This playbook only auto-enables the two to-device flags for Meowlnir/Hookshot (`group_vars/matrix_servers`, ~line 5627), so a JMP Chat deployment must turn them on for this bridge.
5. Key events addressed to it: senders derive targets from joined member events' `device_id`. The current js-sdk does not require cross-signing and does not filter targets by trust; `encryptToDeviceMessages` simply sends to every listed device it knows about and logs unknown devices (`matrix-js-sdk/src/rust-crypto/rust-crypto.ts:1572-1610`).
6. No cleartext-key rejection problem: because the bridge device is a real Olm device, the key arrives inside `m.room.encrypted` to-device content, which is what receivers require.

Trust/verification findings (important, but currently favorable):

- Receive side, Rust crypto (used by Element Web/Element X through `matrix-sdk-crypto-wasm` v18.9.0): to-device decryption uses `DecryptionSettings.sender_device_trust_requirement`; the wasm binding documents that **if the caller passes no settings, `Untrusted` is used**, and js-sdk passes no settings for sync processing (`src/machine.rs` `receive_sync_changes`; `matrix-js-sdk/src/rust-crypto/rust-crypto.ts:1746`). So by default the bridge's (unverified) device can send key events that JS clients accept.
- `matrix-sdk-crypto` has the stricter machinery (`TrustRequirement::CrossSigned`/`CrossSignedOrLegacy`, `olm/account.rs is_from_verified_device_or_allowed_type`) and MSC4143/MSC4153 recommend discarding keys from non-cross-signed sender devices, but the current js-sdk `ToDeviceKeyTransport` implements no such check. This is a policy gap that could close; treat as a medium-term risk, not a current blocker.
- Send side: no trust filtering either; the sender needs a claimable Olm session with the bridge device, which is just key publishing.

Two concrete gaps to build (both small, both localized):

1. `mautrix.OlmMachine.HandleEncryptedEvent` returns decrypted events of unknown inner types, but `HandleToDeviceEvent` (the AS-listener entry point) **drops** them ("Unhandled to-device event"). The bridge needs a hook (small upstream patch or a custom AS event processor handler that calls the machine and inspects the returned `*DecryptedOlmEvent`) to route `io.element.call.encryption_keys` / `m.rtc.encryption_key` to a key table.
2. The bridge's ghost device needs an Olm machine of its own. mautrix-go's `cryptohelper` is bot-oriented and assumes one machine; per-ghost machines or a dedicated "call device" per ghost are new plumbing, but `appservice.IntentAPI` already exposes the raw `mautrix.Client` needed for it (`mrtc-sip-bridge/pkg/hostkit/ghostintent.go` keeps `as.Matrix`).

### 1.5 mrtc-sip-bridge v0.3.1: exact integration points

The bridge is already on `lksdk` v2.18.1, i.e. it already links the LiveKit frame codec. Current pipeline:

- LiveKit leg: `pkg/rtc/livekit.go`.
  - Join: `lksdk.NewLocalSampleTrack(Opus 48k/2ch)` + `room.Publish` (publish options `Source: MICROPHONE`, `DisableDTX`) around lines 186-236.
  - Inbound: `OnTrackSubscribed` → `session.addTrack(identity, remoteReader{*webrtc.TrackRemote})` → `readLoop` reads raw RTP, unmarshals `rtp.Packet`, and delivers `media.RTPPacket{Payload}` to the installed `media.PacketReceiver` (`readLoop`, lines 454-478). `SetReceiver` is the only hook.
  - Outbound: `rtcLeg.WriteOpus(frame, duration)` → `LocalSampleTrack.WriteSample` (lines 480-493).
- Media bridge: `pkg/media/bridge.go`. Inbound path: `ReceiveRTP` → jitter buffer → `refill` → Opus decode → µ-law → SIP. Outbound path: SIP RTP → µ-law decode → `sipToRTCStep` → Opus encode → `rtc.WriteOpus`.
- Crypto integration points with the codec APIs already available:
  - Outbound: pass `lksdk.WithFrameEncryptor(enc)` to `NewLocalSampleTrack` and implement a `KeyProvider` (per-call: our member key, current index). `GCMFrameEncryptor` picks the KID from `CurrentKeyIndex()`.
  - Inbound: either decrypt per packet in `readLoop` (payload trailer gives IV and KID; AAD is the first byte) or replace the reader with `e2ee.NewTrackDecryptor(track, decryptor).ReadSample()` from `OnTrackSubscribed`; the latter also handles depacketization and server-injected frames via `Room.SifTrailer()`. Key provider per remote participant identity, fed from the key table.
  - The decryptor/cache already avoids per-frame re-derivation except on key change (`e2ee/frameencryptor.go` caches cipher blocks).
- Seams for WS-B: `pkg/rtc/rtc.go` `JoinParams` already carries `Ghost`, `RoomID`, `DeviceID`, and `Session.Audio() media.RTCLeg`; `pkg/calls/setup.go` calls `rtc.JoinParams{...}` (line ~364) and `cfg.StartMedia(..., ev.Media, c.session.Audio())` (line ~541); `pkg/calls/manager.go` injects `media.Start`. A key table would be owned by the call manager/call and passed into the `rtc` package via a new field or interface.
- The refusal branch to replace (WS-D): `pkg/calls/setup.go` lines 117-123 (`Encrypted-room guard (D18)`) and `pkg/calls/outbound.go` lines 142-147; the room flag comes from `pkg/hostkit/room.go` `RoomEncrypted` (reads `m.room.encryption` via the bridgev2 matrix connector state store).
- The bridge core has no crypto code today; it builds with `-tags "nocrypto nolibopusfile"` and forces `encryption.default: false` (`cmd/mrtc-sip-bridge/main.go:41-58`, README).

## 2. Unknowns and assumptions

Marked as assumptions unless noted:

1. **Membership encryption churn (biggest unknown).** MSC4143 says member events MUST be encrypted in encrypted rooms and unencrypted ones MUST be treated as left. Current Element Call defaults to Compatibility mode (plaintext state-event memberships) and current matrix-js-sdk enforces nothing, so today the ghost's state event should work and receive keys. If/when clients adopt MSC4354 sticky `m.rtc.member` + `m.rtc.slot` encryption strictly, the bridge must also send *encrypted* member events (megolm outbound), which is a substantially larger change. M2 should test against the versions users actually run and the spike should track this.
2. **Trust enforcement drift.** Defaults today accept unverified devices (wasm `Untrusted`), but clients could enable stricter `TrustRequirement`, or a future js-sdk could implement MSC4153 for key events. Then the bridge device must be cross-signed/verified. No verification UX was prototyped; unbounded effort. M2 should record whether the used clients require it.
3. **Key length.** MSC4195 text says >= 32 bytes, the reference implementation uses 16 (`RTCEncryptionManager.generateRandomKey`, MSC4195 appendix). The bridge must accept any length >= 16 and run HKDF; do not assume 16.
4. **Ratchet support.** Not needed to interoperate with current senders (no implicit sender ratchet). If a sender ever ratchets (manual API), the bridge would need `HKDF-Expand(material, salt, info=128z, 32B)` chaining as in `ParticipantKeyHandler.ratchetKey`. Unverified whether any client uses it in the per-member flow; treat as optional.
5. **Element X native calls.** Element X can host Element Call as a widget (same JS stack), but native implementations in Rust/Kotlin could impose different to-device trust handling. Untested here.
6. **Opus framing.** The bridge decrypts per RTP packet (one Opus frame per packet in WebRTC practice). If a client ever sends multi-frame Opus payloads the per-packet path would break; the `TrackDecryptor` sample path is the safe fallback. Assumption, low risk for audio.
7. **AS transaction shape.** Synapse delivers to-device to appservices with `to_device_id` per event (`mautrix/event/events.go`), but this was only verified in mautrix-go's parsing code, not against a live Synapse with both experimental flags on. M2 must confirm end-to-end.
8. **Per-ghost Olm machine lifecycle.** mautrix-go's cryptohelper is bot-oriented; a machine per ghost user or per call needs custom plumbing and store design (no precedent found in the mautrix bridge ecosystem). Effort unknown until M2.
9. **SIF trailer.** `Room.SifTrailer()` exists; whether it is ever non-empty for audio in this deployment is untested. The decryptor path must handle `(nil, nil)`.
10. **Multiple owner devices.** Per-member keys are per user+device; the key table must key by member/device, not by user. The call already only subscribes the owner's tracks by identity, but several owner devices may join.

## 3. Feasibility verdicts

- **WS-A (Go codec): feasible, low risk — no pure implementation needed from scratch.** The format is documented in code and already implemented in the Go SDK the bridge depends on (`lksdk.EncryptGCMAudioSample*`, `DecryptGCMAudioSample*`, `e2ee.GCMFrame*`, `TrackDecryptor`). Remaining work is a `KeyProvider` that (a) selects keys by KID per participant, (b) derives AES keys with HKDF (salt `LKFrameEncryptionKey`, 128-byte zero info) from the generic secret, and (c) tracks the outbound key index. The RFC 9605 route (including the `sframe` Rust crate by TobTheRock, or cgo) would produce a codec that LiveKit clients cannot read — **do not use it**.
- **WS-B (keys as appservice device): feasible, medium risk; this is where the spike's fate is decided.** Hard requirements are all implementable with existing components: MSC4190 device creation (Synapse + mautrix-go), MSC4326/MSC3202 masquerading (Synapse default-on + mautrix-go flag), MSC2409/MSC3202 transaction extensions (Synapse flags this deployment must enable), Olm device + key upload (goolm), a joined membership whose device id is the bridge device, and encrypted to-device send/receive. Current clients send keys to any known device of a joined member without trust filtering, and js-sdk accepts keys from unverified devices by default. Two build items: routing unknown decrypted to-device types to the call core, and per-ghost Olm machine management. Main risks are policy churn (encrypted memberships, cross-signing requirements), not protocol impossibility.
- **WS-C (media integration): feasible, low risk once keys exist.** Both hook points already exist and are narrow: `lksdk.WithFrameEncryptor` at the single `NewLocalSampleTrack` call, and decryption either inside `readLoop` or by swapping the reader for `e2ee.TrackDecryptor`. The key table needs a path from the call/host into `pkg/rtc` (`JoinParams` is the natural seam). No changes to Opus/µ-law/jitter code are needed. Video untouched as designed.

## 4. Recommended next steps

- **M1 (Go codec, 2-3 days):** build a `KeyProvider` + conversion layer around `lksdk`/`e2ee`; do not write crypto primitives. Test vectors: capture frames produced by `livekit-client` (its own vitest `FrameCryptor`/`ParticipantKeyHandler` tests show the construction and deterministic ratchet), plus a HKDF known-answer check against the JS `deriveKeys` inputs. Cross-check both directions: decrypt JS output, and confirm a JS `FrameCryptor` decrypts Go output (treat 1 cleartext TOC byte, 12-byte IV, trailer `[12, KID]` as the contract).
- **M2 (key receipt prototype, 3-5 days):** stand up Synapse with `msc2409_to_device_messages_enabled` and `msc3202_transaction_extensions` enabled; create a ghost device via `CreateDeviceMSC4190`/MSC4190; publish device keys/OTKs; publish a membership carrying that device id; run two Element Call clients in an encrypted room; capture and decrypt the to-device key events; verify key rotation on join/leave and index wrap. Record which client versions/trust settings deliver keys. Decide the ghost-device identity model (per-ghost vs one call device) here.
- After M1+M2: M3 media round-trip in a live call (both directions), including the outbound key distribution from the bridge so clients can decrypt the phone side.
- **Deployment work item (not spike):** extend the auto-enable condition for the two Synapse flags in `group_vars/matrix_servers` (~line 5627) to cover this bridge, and register the appservice with MSC3202/MSC4190 flags (cf. the mautrix bridge roles' registration templates).

## 5. Stop-criteria check

The spike's stop condition was: stop if keys are not sent to appservice devices, or the codec cannot be matched.

- Codec: **not a stopper.** The exact format is implemented in a Go library already in the bridge's dependency tree; matching is a matter of wiring, with primary-source code to mirror and test vectors available.
- Key receipt: **no blocker found.** Key events are Olm-encrypted to-device messages; the sender picks recipients from joined member devices and does not currently apply trust filtering; Synapse and mautrix-go support every required appservice device primitive. The remaining work is integration plumbing and field validation, both timeboxed.
- Conclusion: **M1 and M2 look worth attempting.** The riskiest item is not crypto but client-policy churn (encrypted memberships, cross-signing requirements); M2's job is to measure that risk against the clients actually used, and the fallback (`call-room`) stays valid throughout.
