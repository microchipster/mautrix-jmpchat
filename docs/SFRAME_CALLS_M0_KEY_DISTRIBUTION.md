<!--
SPDX-FileCopyrightText: 2026 Slavi Pantaleev

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# SFrame calls M0: key distribution, device identity, codec

Decision document for [SFRAME_CALLS_SPIKE.md](SFRAME_CALLS_SPIKE.md), following
[SFRAME_CALLS_M0_FINDINGS.md](SFRAME_CALLS_M0_FINDINGS.md). This pass reads the current
implementations and answers the four M0 questions with file-level references. Read-only research;
no bridge code changed. Pinned sources (inspected 2026-10-28):

- `matrix-org/matrix-js-sdk` @ `6699a308` (`src/matrixrtc`, `src/rust-crypto`)
- `element-hq/element-call` @ `6793a4b4` (`sdk/src/matrixrtc`, `sdk/src/media-backend/livekit`)
- `livekit/client-sdk-js` @ `520c1440` (`src/e2ee`)
- `matrix-org/matrix-rust-sdk` @ `cdfdcc91` (`matrix-sdk-crypto`, `matrix-sdk-base`)
- `livekit/server-sdk-go/v2` v2.18.1 (already a dependency of `mrtc-sip-bridge` v0.3.1)
- `maunium.net/go/mautrix` v0.32.0
- `element-hq/synapse` `develop` (fetched 2026-10-28)
- `matrix-org/matrix-spec-proposals` @ `a6d4f68` (MSC4143, MSC4195, MSC4190, MSC4326)
- RFC 9605 (for contrast only)

Placeholders used throughout: `@alice:example.com`, `!room:example.com`, `DEVICE`, `BRIDGEDEV`,
`https://matrix.example.com`, `https://sfu.example.com`.

## Verdict

- **M1 (Go codec): GO.** The "SFrame" that Element Call uses is LiveKit's frame format, not RFC 9605.
  It is already implemented in `lksdk`/`lksdk/e2ee`, which the bridge links today. The remaining work
  is a `KeyProvider` and a thin wrapper, not crypto. A cross-implementation known-answer test
  (JS WebCrypto encrypt → Go SDK decrypt) passed locally (vector in §3.4).
- **M2 (key receipt as an appservice device): GO, medium risk.** Every primitive exists:
  MSC4190 device creation, MSC4326/MSC3202 masquerading, goolm, Olm to-device in appservice
  transactions, and clients that send keys to every device of every joined membership without trust
  filtering. The work is plumbing. The risk is client policy drift, not protocol impossibility.
- **M3 stays conditional on M2** measuring the clients users actually run (see §7).

Two findings change the prior plan:

1. mautrix v0.32 does **not** need an upstream patch to expose unknown decrypted to-device types.
   `OlmMachine.HandleEncryptedEvent` already returns them; the bridge must register its own handler
   for `event.ToDeviceEncrypted` instead of using `AddAppserviceListener` for that one type (§2.4).
2. Publishing the encrypted track must set `TrackPublicationOptions.Encryption` to
   `livekit.Encryption_GCM`. Otherwise the SFU advertises `TrackInfo.encryption = NONE`, and Element
   Call's `unencryptedWarning$` flags the bridge's audio as unencrypted even though the frames are
   encrypted (`src/state/media/MemberMediaViewModel.ts:93-107`, §6 item F3).

Settled before this pass, re-confirmed here: the codec is LiveKit's format (`lksdk` already has it),
keys travel as Olm-encrypted to-device, and appservice devices are a supported primitive. Newly
settled here: the exact payload, rotation timing, recipient rules, identity strings, trust defaults,
and the fact that no mautrix patch is needed. Still open and assigned to M2: the clients users
actually run (versions, Compatibility vs sticky mode, Element X iOS native stack), and whether their
trust policy changes. See §7.

## 1. Key distribution as Element Call implements it

### 1.1 Event type and encryption requirement

- The event that carries media keys today is the **unstable** to-device type
  `io.element.call.encryption_keys` (`EventType.CallEncryptionKeysPrefix`,
  `matrix-js-sdk/src/@types/event.ts:124`).
- It is sent as an Olm-encrypted to-device message, i.e. the wire type is `m.room.encrypted`,
  algorithm `m.olm.v1.curve25519-aes-sha2`. Receivers **ignore cleartext** keys:
  `ToDeviceKeyTransport.ts:170-174`.
- MSC4143 defines the stable name `m.rtc.encryption_key`, but the reference clients do not send it
  yet. The MSC explicitly lists `io.element.call.encryption_keys` as the unstable identifier
  (MSC4143, "Unstable prefix").
- Receiving only checks the outer wire type, `room_id`, and `member.claimed_device_id`
  (`ToDeviceKeyTransport.ts:162-219`). It does **not** validate the sender against the membership
  event. MSC4143's appendix admits this: the reference implementation performs "no validation of
  received to-device messages against `m.rtc.member` events" (MSC4143, "Implementation differences
  in the reference implementation"). A bridge must do that validation itself.

### 1.2 Payload schema (exact)

`RTCEncryptionManager.sendKey` (`RTCEncryptionManager.ts:91-107`) builds:

```json5
{
  "room_id": "!room:example.com",
  "member_id": "@alice:example.com:DEVICE",
  "media_key": { "index": 0, "key": "AAAAAAAAAAAAAAAAAAAAAA" },
  "keys":      { "index": 0, "key": "AAAAAAAAAAAAAAAAAAAAAA" },
  "member": {
    "claimed_device_id": "DEVICE",
    "id": "@alice:example.com:DEVICE"
  }
}
```

- `media_key` is current; `keys` is the deprecated duplicate. Both are sent "for backwards
  compatibility" (`RTCEncryptionManager.ts:100-101`, types in `matrixrtc/types.ts:78-122`).
- `member.claimed_device_id` is **required** by the current receiver. It exists because Element Web
  embeds Element Call as a widget and the widget API does not expose encryption metadata
  (`types.ts:104-113`, MSC4143 appendix).
- `member_id` / `member.id` is the membership's `member.id` (legacy mode:
  `"<user>:<device>"`; sticky mode: a UUID).
- An older, still-declared shape exists: `{ room_id, device_id, call_id, keys: {index,key}, sent_ts }`
  (`matrixrtc/types.ts:65-71`). Current receivers reject it because `claimed_device_id` is missing.
  The bridge should accept it anyway when parsing, and always send the current shape.

Key material:

- `key` is raw bytes, base64. The MSC says unpadded base64; the reference implementation actually
  calls the padded `encodeBase64` (`RTCEncryptionManager.ts:24,468`). `decodeBase64` is loose
  (`lastChunkHandling: "loose"`), so padded and unpadded both decode. Bridge: accept both, emit
  unpadded.
- `index` is an integer 0..255 and is used directly as the SFrame KID (`types.ts:114-121`).
- The secret is **not** an AES key. It is HKDF input material (§3.2).

### 1.3 Recipient selection

`RTCEncryptionManager.rolloutOutboundKey` (`RTCEncryptionManager.ts:388-423`) maps every joined
membership to `{userId: membership.sender, deviceId: membership.deviceId}` and filters out its own
user+device. `ToDeviceKeyTransport.sendKey` then calls
`client.encryptAndSendToDevice(EventType.CallEncryptionKeysPrefix, targets, content)`
(`ToDeviceKeyTransport.ts:109-123`).

Consequences:

- **Every device** of every joined member gets a copy, one Olm-encrypted to-device event per device.
- **No trust filtering** on the send side. `encryptToDeviceMessages`
  (`matrix-js-sdk/src/rust-crypto/rust-crypto.ts:1572-1610`) resolves each target with
  `olmMachine.getDevice()`; devices with no uploaded device keys are skipped and logged, but no
  signature/verification check is applied.
- **Appservice/bot devices are included** iff they authored a membership and published device keys.
  There is no "bot" exclusion.
- Devices that joined the room but not the call get nothing (memberships only).

This is the property M2 depends on: a ghost that (a) is joined to the room, (b) has a membership
whose `device_id` names a real Olm device, and (c) has published device keys/OTKs will be a target.

### 1.4 Rotation semantics (current code, not the MSC)

Constants and defaults:

| Setting | Value | Source |
| --- | --- | --- |
| Random key length | 16 bytes, fixed | `RTCEncryptionManager.ts:517-521` |
| First index | 0 | `:371-376` |
| Next index | `(index + 1) % 256` | `:510-515` |
| Join grace (reuse current key) | 10 s | `:106`, `:268` |
| Delay before using a new key | 1 s in Element Call (constructor default 5 s) | `:96`, `:267` |
| Participant limit for rotation | unset by default (no suppression) | `:48`, `:269-270` |
| Periodic rotation | **none implemented** | no timer in `:367-494` |

Timeline on `onMembershipsUpdate` (`:357-365`), which runs on every membership change:

1. First distribution: create key index 0, add it locally, send to all current members, use
   immediately (`hasKeyChanged` is false, so no sleep).
2. A member **left** (present in `sharedWith`, absent from current memberships) → create a new key,
   send to all current members, sleep `useKeyDelay`, then use it (`:439-443`, `:477-490`).
3. A member **joined**:
   - key age < 10 s → share the current key with the joiner only (`:444-451`);
   - key age ≥ 10 s → rotate to everyone (`:452-457`).
4. A membership with the same user+device but a different `membershipTs` is treated as a rejoin:
   `sharedWith` is cleared for it (`:404-410`), then rule 3 applies.
5. Index wrap at 256. Keyring on the client side is 256 entries
   (`element-call/.../MatrixKeyProvider.ts:19`), so this is safe for normal call lengths.
6. MSC4143 recommends periodic rotation; the implementation does not do it. Do not build timing
   assumptions around periodic rotation.

`OutdatedKeyFilter` (`matrixrtc/utils.ts:26-50`) drops a key with the same index only when its
timestamp is older than one already seen. A bridge key table can simply overwrite the slot for an
index.

### 1.5 Identity mapping (which string keys the key table)

`EncryptionManager.getEncryptionKeyMapKey` (`EncryptionManager.ts:9-11`) uses
`<userId>:<deviceId>(<memberId>)` for storage, but the media layer receives a `backendIdentity`:

- legacy transport (`foci_preferred[].livekit_service_url`, what this deployment and the bridge use):
  `"<userId>:<deviceId>"` (`RTCEncryptionManager.ts:191-193`,
  `CallMembership.ts:146-155`). This is exactly the bridge's existing LiveKit identity
  (`pkg/rtc/livekit.go:220-222` already derives and warns on it).
- MSC4195 `url` transport: `Base64(SHA256(CanonicalJSON([sender, member.id])))`, unpadded
  (`RTCEncryptionManager.ts:187-190`; MSC4195 "LiveKit participant identities";
  `computeRtcIdentityRaw` in `matrixrtc/membershipData/rtc.ts:186-192`).
- If a membership publishes both transport variants, both identities are produced
  (`CallMembership.ts:146-158`).

**Bridge design consequence:** store received keys under every identity derivable from the sender's
membership, and match inbound RTP by the subscribed track's LiveKit participant identity. Then
transport migration (legacy → `url`) does not require a key-table rewrite. Hashing
`json.Marshal([]string{sender, memberID})` in Go reproduces the canonical JSON used by JS.

### 1.6 Trust rules on the receive side

- **JavaScript stack (Element Web, Element Call, Element X Android via WebView):**
  to-device sync is processed by `receiveSyncChanges` with no `DecryptionSettings` parameter
  (`matrix-js-sdk/src/rust-crypto/rust-crypto.ts:1746`). The wasm binding therefore uses
  `TrustRequirement::Untrusted`, and `matrix-sdk-crypto` accepts the event
  (`matrix-sdk-base/src/client.rs:232-235` is the Rust default; the JS call does not override it).
  Device isolation modes (`AllDevicesIsolationMode` vs `OnlySignedDevicesIsolationMode`) apply to
  **room event** decryption (`rust-crypto.ts:2307-2321`), not to to-device events.
- **Rust SDK default:** `TrustRequirement::Untrusted` (`matrix-rust-sdk`
  `crates/matrix-sdk-base/src/client.rs:232-235`, builder default
  `client/builder/mod.rs:176-180`); to-device trust enforcement lives in
  `olm/account.rs:1642-1690`. No Element X code found that overrides it.
- MSC4143 says clients SHOULD discard key events from devices that are not cross-signed by their
  owner (MSC4153). **Current JS does not implement this check** in `ToDeviceKeyTransport`.
- Consequence: an unverified appservice device is accepted today. Treat this as a policy risk, not
  a fact of life; see §2.5 for the escalation path.

### 1.7 Does the bridge's own audio need to be marked encrypted?

Yes, at the signalling layer. Element Call reads `publication.isEncrypted` from the LiveKit
`TrackInfo.encryption` field (`LivekitMediaTrack.ts:117`, livekit-client
`TrackPublication.ts:99,144`). The Go SDK only sends that field if
`TrackPublicationOptions.Encryption` is set (`localparticipant.go:128-139`,
`publication.go:628-646`); a frame encryptor alone does not set it. Element Call's own publisher uses
`Encryption_Type.GCM` (`LocalParticipant.ts:414-421`). The bridge must publish with
`Encryption: livekit.Encryption_GCM`.

## 2. Can an appservice ghost obtain these keys?

### 2.1 What "addressable" requires

1. The ghost user exists and is joined to the room (it already sends membership state events).
2. A device exists for the ghost with the exact `device_id` used in its membership content
   (`m.call.member` `device_id`, legacy mode).
3. That device has published device keys (self-signed) and one-time keys, so senders can run
   `/keys/query` and `/keys/claim`.
4. The bridge can masquerade as that device on `/keys/upload`, `/keys/query`, `/sendToDevice`.
5. The homeserver delivers to-device messages for that device to the appservice.
6. The bridge holds the Olm account for that device and can decrypt.

### 2.2 mautrix-go v0.32 status

| Primitive | Where | Status |
| --- | --- | --- |
| Create device as appservice (MSC4190) | `client.go:1185-1198` `CreateDeviceMSC4190` | present |
| Device masquerading (MSC4326/MSC3202) | `client.go:137`, `url.go:115-121` (`device_id` + `org.matrix.msc3202.device_id`) | present |
| Olm machine + goolm | `crypto/machine.go:127`, `crypto/registergoolm.go` (`goolm` build tag) | present |
| Per-device crypto store | `crypto/sql_store.go:55-63` (`accountID`, `deviceID`) | supports multiple devices |
| Receive to-device from AS transactions | `crypto/machine.go:280-296` `AddAppserviceListener` | present, with one caveat (§2.4) |
| Send Olm to-device | `crypto/encryptolm.go:20-80` `EncryptToDevices` | present |
| Device-key + OTK upload | `crypto/machine.go:871-960` `ShareKeys` | present |
| Cross-signing bootstrap | `crypto/cross_sign_key.go:86`, `cross_sign_signing.go:71,122` | present |

Synapse side:

- `PUT /_matrix/client/v3/devices/{deviceId}` creates/updates a device for an appservice
  (`synapse/rest/client/devices.py:206-218`).
- `device_id` in the AS query string is validated against an existing device, else
  `M_UNKNOWN_DEVICE` (`synapse/api/auth/base.py:342-393`).
- To-device messages are included in transactions only with
  `experimental_features.msc2409_to_device_messages_enabled` (MSC4203; the flag name is historical)
  and only for appservices whose registration sets `receive_ephemeral`
  (`synapse/handlers/appservice.py:281-290, 316-320`); the payload carries `to_user_id` and
  `to_device_id` (`synapse/handlers/appservice.py:620-650`).
- Device lists / OTK counts additionally need `msc3202_transaction_extensions`
  (`synapse/config/experimental.py:84-93`).

mautrix registration/config knobs:

- `appservice.ephemeral_events: true` generates both `receive_ephemeral` and the unstable
  `de.sorunome.msc2409.push_ephemeral` (`bridgev2/bridgeconfig/appservice.go:74-86`;
  `appservice/registration.go:29-30`).
- `encryption.appservice: true` and `encryption.msc4190: true` generate the MSC3202/MSC4190
  registration flags (`bridgev2/bridgeconfig/appservice.go:83-86`); they do **not** enable
  end-to-bridge encryption. Keep `encryption.allow/default` false.

### 2.3 The device-ID model is currently a liability

`pkg/calls/callid.go:40-48` invents a **new device ID per call**:
`"SIP" + last 8 characters of the call id`. The membership publishes it
(`pkg/matrixrtc/membership.go:44-70`), and LiveKit uses it for the identity
(`pkg/rtc/livekit.go:220`). That means one real Matrix device, one Olm account, one set of OTKs and
one device-list entry per call, forever, unless deleted.

Recommendation for M2/M3: one stable call device per **ghost** (for example `BRIDGEDEV`), created
lazily and reused. The membership state key and LiveKit identity become stable, no device churn, and
the Olm account persists. One device can serve sequential calls; concurrent calls for the same ghost
are not a real scenario (one phone number, one line). If per-call devices are kept, delete them via
MSC4190 after the call and drop the local crypto store.

### 2.4 Receiving keys without patching mautrix (confirmed)

The prior findings said this needs a hook or upstream patch. It does not. In v0.32:

- `OlmMachine.HandleEncryptedEvent` decrypts and, for inner types it does not know, logs
  `"Unhandled encrypted to-device event"` and **returns the decrypted event**
  (`crypto/machine.go:445-495`). It returns `nil` for types it handled.
- `HandleToDeviceEvent` calls it and throws the return value away
  (`crypto/machine.go:529-552`).

So the bridge should **not** use `AddAppserviceListener` for `event.ToDeviceEncrypted`. Instead it
should register, in `PostInit`:

```go
ep.On(event.ToDeviceEncrypted, func(ctx context.Context, evt *event.Event) {
    if evt.ToDeviceID != callDeviceID { return } // events for other ghost devices
    de := mach.HandleEncryptedEvent(ctx, evt)
    if de != nil && de.Type.Type == "io.element.call.encryption_keys" {
        keys.Ingest(de) // de.Content.VeryRaw / Raw holds the decrypted JSON
    }
})
```

Register the other handlers `AddAppserviceListener` would add (`m.room_key_request`,
`m.room_key.withheld`, verification events, beeper ack) individually, plus
`ep.OnOTK(mach.HandleOTKCounts)` and `ep.OnDeviceList(mach.HandleDeviceLists)`. This preserves every
machine behaviour and adds none of the double-decrypt problems. `mrtc-sip-bridge` already has the
registration seam: `pkg/connector/rtchook.go` + `pkg/core/hook.go`, called from
`cmd/mrtc-sip-bridge/main.go:60`.

### 2.5 Trust escalation, if clients tighten policy

If a future client enforces "cross-signed only" for key events, the bridge can satisfy it without
human verification:

- Synapse lets an appservice upload/replace cross-signing keys without UIA
  (`synapse/rest/client/keys.py:536-590`; MSC4190).
- mautrix can generate the ghost's cross-signing keys and sign its own device
  (`crypto/cross_sign_key.go:86`, `crypto/cross_sign_signing.go:122`).
- `TrustRequirement::CrossSigned` accepts `VerificationLevel::UnverifiedIdentity`, i.e. a master key
  that exists and signs the device but has not been verified by another user, including for
  to-device events (`matrix-sdk-crypto/src/olm/account.rs:1933-1945`,
  `src/machine/mod.rs:2315-2385`).

This is extra work, not a dead end. It should not be built in M1/M2.

### 2.6 Remaining feasibility risks

| Risk | Assessment |
| --- | --- |
| Client stops sending to unverified devices | Possible; two-line policy change in js-sdk. Mitigation: cross-signing path above. |
| Client starts validating key sender against membership | Bridge already matches membership device/ID, so it passes. |
| MSC4354 sticky `m.rtc.member` becomes default | Bigger change: bridge must send encrypted sticky membership events, not state events. Current EC defaults to Compatibility (`sdk/src/config.ts:12-22`, `src/settings/settings.ts:152-155`). Test in M2. |
| Device list not yet learned by sender | First key send can be dropped (`unknown device` warning). Retry happens on the next membership-triggered rotation; M2 should confirm. |
| Multiple AS devices for one ghost | Machine per device; filter `to_device_id`; no shared account. |

## 3. Wire format and crypto to implement in Go

### 3.1 The format actually on the wire

LiveKit's frame format, with the header at the **end** (its code calls it "similar to the header
described in draft-omara-sframe-00, but we put it at the end",
`livekit-client/src/e2ee/worker/FrameCryptor.ts:514-525`):

```
 +------------------+-------------------------------+------------+------+-----+
 | Opus TOC (1 B)   | AES-128-GCM ciphertext + tag  | IV (12 B)  | 12   | KID |
 | cleartext, AAD   | (plaintext_len - 1 + 16)      | cleartext  | 1 B  | 1 B |
 +------------------+-------------------------------+------------+------+-----+
```

- Overhead: 30 bytes (1 header + 16 GCM tag + 12 IV + 1 length + 1 KID).
- AAD: the single cleartext TOC byte (`UNENCRYPTED_BYTES.audio = 1`,
  `livekit-client/src/e2ee/constants.ts:18-23`).
- IV: 12 bytes, transmitted. The JS sender derives it from SSRC + RTP timestamp + a per-SSRC
  counter (`FrameCryptor.ts:886-905`); the Go SDK uses `crypto/rand`. Receivers must use the
  transmitted IV, so any 12-byte value is valid.
- KID: last byte, indexes the per-sender key ring. 0..255.
- Empty (DTX) frames are not encrypted in JS (`FrameCryptor.ts:469-471`); the bridge publishes with
  `DisableDTX` already (`pkg/rtc/livekit.go:65-67`), but the Go encryptor must still guard against
  zero-length samples.
- Server-injected frames (SIF) are plaintext and end with `Room.SifTrailer()`; drop them
  (`lksdk/encryption.go:90-100`, `lksdk/e2ee/frameencryptor.go:140-150`).

Go SDK entry points (v2.18.1):

- `lksdk.EncryptGCMAudioSample` / `...CustomCipher` (`encryption.go:200-260`)
- `lksdk.DecryptGCMAudioSample` / `...CustomCipher` (`encryption.go:88-180`)
- `e2ee.GCMFrameEncryptor` / `GCMFrameDecryptor` (`e2ee/frameencryptor.go:33-170`)
- `lksdk.NewFrameEncryptor/NewFrameDecryptor(kp, lksdk.CodecOpus)` (`lksdk/e2ee.go:57-78`)
- `lksdk.WithFrameEncryptor` on `NewLocalSampleTrack` (`localtrack.go:115-119, 527-537`)

`GCMFrameDecryptor` reads the KID from the trailer and calls `KeyProvider.GetKey(kid)`
(`e2ee/frameencryptor.go:131-150`), so the bridge only has to implement
`GetKey(uint32) ([]byte, error)` and `CurrentKeyIndex() uint32`
(`e2ee/types/interface.go:18-23`).

### 3.2 Key derivation

The Matrix `media_key.key` bytes are HKDF **input key material**, not the AES key:

```
aes_key = HKDF-SHA256(
    ikm  = media_key.key,
    salt = "LKFrameEncryptionKey",
    info = 0x00 * 128,
    L    = 16)
```

Confirmed in JS (`src/e2ee/utils.ts:70-115`), Go (`lksdk/encryption.go:53-84`,
`LIVEKIT_HKDF_INFO_BYTES = 128`, `LIVEKIT_KEY_SIZE_BYTES = 16`), and MSC4195
("End-to-end encryption": key size 128 bits, keyring 256, ratchet salt
`LKFrameEncryptionKey`). MSC4195 says the secret must be at least 32 bytes, but the reference
implementation generates 16 (MSC4195 appendix; `RTCEncryptionManager.ts:517-521`). Accept any length
≥ 16; emit 16 for maximum parity.

### 3.3 Ratchet

Two different things are called "ratchet":

1. **MatrixRTC key rotation** — a new random secret at index+1, distributed over to-device (§1.4).
   This is the only mechanism current senders use.
2. **LiveKit receive-side ratchet** — on decrypt failure, the receiver derives new material
   `HKDF-SHA256(ikm = current_material, salt = "LKFrameEncryptionKey", info = 0x00*128, L = 32)`,
   re-imports it as HKDF material, re-derives the AES key, and retries up to
   `ratchetWindowSize` (10 in Element Call) (`ParticipantKeyHandler.ts:114-149`,
   `FrameCryptor.ts:800-840`). It is local to the receiver and is never signalled.

Senders do not auto-ratchet. The bridge does not need (2) to interoperate; if a sender ever does
ratchet manually, the frames become undecryptable until a new Matrix key arrives. Low risk; optional
future work.

### 3.4 Test vectors

HKDF known-answer test (verified locally against both Node WebCrypto and `lksdk.DeriveKeyFromBytes`):

```
secret  = 000102030405060708090a0b0c0d0e0f
aes_key = 4086b4641064e1ae8b63d4eb83ad3e7e
```

Full frame (JS WebCrypto encryption of a 20-byte fake Opus packet, fixed IV, KID 7; decrypted back to
the plaintext by `lksdk.DecryptGCMAudioSample`):

```
plaintext = f80102030405060708090a0b0c0d0e0f10111213
frame     = f83524ddffa627b64c723a6827f452b6815aa11ba7a509b49b16fa6bab7ec5138f36fa77
            000102030405060708090a0b0c07
```

Note that this only proves format + HKDF + AEAD; it does not replace a capture from a real
`livekit-client`. Corpus sources, in order of value:

1. `livekit-client/src/e2ee/worker/FrameCryptor.test.ts` and `ParticipantKeyHandler.test.ts` —
   deterministic key/IV construction and the encrypt/decrypt round trip.
2. `livekit/server-sdk-go/v2/encryption_test.go` — fixed AES key + `opusEncryptedFrame` /
   `opusDecryptedFrame` vectors for the exact wire layout.
3. A real call capture: dump RTP payloads plus the corresponding to-device key (test room, two
   clients), then assert Go decrypts them. Do this in M2/M3.
4. `livekit/rust-sdks` delegates frame crypto to `libwebrtc::frame_cryptor`; it is a useful
   cross-implementation check but not a vector corpus.
5. RFC 9605 Appendix C vectors and `chris-wood/sframe-go` are **not** applicable to this format.

### 3.5 RFC 9605 is a different protocol

Do not implement the RFC for this interop path. Differences that matter:

| Aspect | RFC 9605 | LiveKit / MatrixRTC today |
| --- | --- | --- |
| Header | front, KID/CTR varints, X/Y flags | trailer, 1-byte KID, no CTR |
| Nonce | `encode(CTR) XOR salt` | IV transmitted in the frame |
| Key derivation | per-KID labels `"SFrame 1.0 Secret key "` + KID + suite | one HKDF from the Matrix secret |
| AEAD | AES-CTR+HMAC and AES-GCM suites | AES-128-GCM only |
| Ratchet | `HKDF(..., "SFrame 1.0 Ratchet", Nh)` | HKDF with `LKFrameEncryptionKey`, local to receiver |
| Metadata | header + metadata AAD | one Opus TOC byte AAD |

A spec-compliant RFC 9605 implementation produces frames that LiveKit clients cannot read, and
vice versa. The word "SFrame" in Matrix/LiveKit documentation refers to the LiveKit format.

## 4. Existing Go implementations

| Module | License | Format | Verdict |
| --- | --- | --- | --- |
| `github.com/livekit/server-sdk-go/v2` (`lksdk`, `lksdk/e2ee`) | Apache-2.0 | LiveKit format | **Use it.** Production code, already in the bridge's dependency tree. Codec yes; key management/ratchet no. |
| `github.com/chris-wood/sframe-go` | MIT | RFC 9605 | ~500 LOC reference implementation, no ratchet, explicitly "not production". Not interoperable with Element Call. Useful only as an RFC reference. |
| `github.com/cisco/sframe` | BSD-2-Clause | RFC 9605 | C++ with test vectors. Not a Go option without cgo. |
| `github.com/TobTheRock/sframe-rs` | MIT/Apache-2.0 | RFC 9605 | Rust; cgo/FFI would add weight for a format we do not use. |
| `github.com/shiguredo/sora-e2ee` | Apache-2.0 | Sora-specific double ratchet | Not LiveKit-compatible. |
| `github.com/npillmayer/pmmp/sframe` | — | unrelated | Ignore. |

No third-party Go implementation of the LiveKit key provider / MatrixRTC key distribution was found.
The bridge's `sframe` package should therefore be thin: base64 + HKDF + key table + `KeyProvider`,
with `lksdk` doing AEAD.

## 5. GO/NO-GO

**GO for M1 and M2.** Stop criteria from the spike:

- *Codec cannot be matched* — disproved. The exact format is in a dependency; the KAT above closes
  the loop JS → Go.
- *Keys not sent to appservice devices* — no protocol blocker. Senders fan out to every membership
  device with no trust filter; Synapse + mautrix provide every primitive to be that device.

Proceed in this order:

1. **M1 (1–2 days):** codec wrapper + KAT + unit tests. No Matrix work.
2. **M2 (3–5 days):** device, key receipt, rotation. Timebox; record client versions and modes.
3. **M3 only after M2** shows keys arriving and rotating in a real encrypted room.

If M2 fails, the most likely causes are configuration (registration flags, Synapse experimental
features) or a client-mode change (sticky memberships), not missing cryptography. The call-room
fallback stays valid throughout.

## 6. Implementation checklist for the bridge

### A. Build and dependencies

- [ ] Build with `goolm` so no libolm is needed: `-tags "nocrypto goolm nolibopusfile"`.
      `nocrypto` only selects bridgev2's no-E2B helper (`bridgev2/matrix/no-crypto.go:7`); it does
      not remove `maunium.net/go/mautrix/crypto`.
- [ ] New package (for example `pkg/sframe`): base64, HKDF, key table, `KeyProvider`. Reuse
      `lksdk`/`e2ee`; no new crypto primitives.
- [ ] Stable storage for the Olm account: `crypto.NewSQLCryptoStore(br.DB, ..., accountID, deviceID,
      pickleKey)` with `accountID = ghost + ":" + device`; pickle key from bridge config.

### B. Device identity and registration

- [ ] One stable call device per ghost (recommended) or per call with cleanup. Create via
      `CreateDeviceMSC4190` (idempotent `PUT`).
- [ ] Dedicated `mautrix.Client` for the call device: same homeserver and AS token, `UserID = ghost`,
      `DeviceID = callDevice`, `SetAppServiceUserID = true`,
      `SetAppServiceDeviceID = true`. Do not mutate the shared per-ghost intent client.
- [ ] `ShareKeys(ctx, -1)` on startup and after OTK count drops; keep at least 50 signed OTKs.
- [ ] Publish membership with `device_id` = that device and `membershipID = "<ghost>:<device>"`
      (already done in `pkg/matrixrtc/membership.go`). Keep `created_ts` stable across expiry
      refreshes; only a new call gets a new `created_ts`.
- [ ] Registration/config: `appservice.ephemeral_events: true`,
      `encryption.appservice: true`, `encryption.msc4190: true`; keep `encryption.allow: false`.
- [ ] Synapse: enable `msc2409_to_device_messages_enabled` and `msc3202_transaction_extensions`.
      Extend the condition in `group_vars/matrix_servers` (~line 5630) to cover this bridge.

### C. To-device subscription

- [ ] In `PostInit`, register `event.ToDeviceEncrypted` on `br.Matrix.EventProcessor` as in §2.4;
      do not call `AddAppserviceListener` for that type. Register its other handlers manually.
- [ ] Filter on `evt.ToDeviceID == callDevice` before decrypting; machines are per device.
- [ ] Parse `de.Content.VeryRaw` (or `.Raw`) for `io.element.call.encryption_keys`; do not require
      mautrix to know the type.
- [ ] Validate: `room_id` matches; sender's membership is joined; `claimed_device_id` equals that
      membership's device; decode `media_key` or `keys`.
- [ ] Accept padded/unpadded standard and URL-safe base64.
- [ ] Buffer keys that arrive before the membership is known, and retry on membership update
      (mirror `keysWithoutMatchingRTCMembership`, `RTCEncryptionManager.ts:212-247`).

### D. Key table and rotation

- [ ] Per room + LiveKit identity: `map[uint8][16]byte` (plus receipt time). Derive and cache
      `cipher.Block` per index.
- [ ] Store each key under both legacy `"<sender>:<device>"` and MSC4195 hashed identity.
- [ ] Prune on call end and on participant leave; cap total entries.
- [ ] Track memberships (`user`, `device`, `created_ts`) and rotate the bridge key on leave and on
      join-after-grace, mirroring §1.4. Simpler and acceptable for 1:1 audio: rotate on any change.

### E. Outbound key distribution

- [ ] Generate 16 random bytes, index 0, add locally, use immediately.
- [ ] Send to every joined membership device except self, with the full §1.2 content (send both
      `media_key` and `keys`, both `member_id` and `member.id`, `member.claimed_device_id`).
- [ ] Use `mach.EncryptToDevices(ctx, event.Type{Type: "io.element.call.encryption_keys",
      Class: event.ToDeviceEventType}, req)` then `SendToDevice`.
- [ ] On rotation: distribute first, wait ≥ 1 s, then bump `CurrentKeyIndex()`. Index =
      `(index + 1) % 256`. Keep the old key available for the delay window.
- [ ] Do not send to devices that have no keys; log and retry on the next membership change.

### F. Media wiring (fork of `mrtc-sip-bridge`)

- [ ] `rtc.JoinParams` (or `rtc.Config`) carries a per-call key source and the call device ID.
- [ ] Create the local track with `lksdk.WithFrameEncryptor(enc)` and
      `lksdk.NewFrameEncryptor(kp, lksdk.CodecOpus)` (`pkg/rtc/livekit.go:186-190`).
- [ ] Publish with `Encryption: livekit.Encryption_GCM` in `realRoom.Publish`
      (`pkg/rtc/livekit.go:64-68`).
- [ ] Inbound: in `readLoop` (`pkg/rtc/livekit.go:454-478`), before `ReceiveRTP`, decrypt the RTP
      payload with the sender identity's key selected by the last byte; drop SIF frames matching
      `Room.SifTrailer()`; on unknown KID/missing key, drop and count (optionally hold ≤ 500 ms).
- [ ] Keep the per-packet path: it preserves RTP seq/timestamp for the existing jitter buffer.
      `TrackDecryptor` is the fallback if multi-packet Opus ever appears.
- [ ] Guard zero-length payloads (DTX) and `len(payload) < 2`.
- [ ] Video untouched; no key handling for video.

### G. UX / fallback

- [ ] Replace the D18 refusal branch (`pkg/calls/setup.go:117-123`,
      `pkg/calls/outbound.go:142-147`) with `calls.e2ee: auto|off`.
- [ ] `auto`: encrypted room + key setup succeeded → SFrame; otherwise fall back to the call room
      with a room notice.
- [ ] `sip-status` reports media E2EE state and the last key error.
- [ ] Ring notifications and membership state events stay plaintext; Element Call also sends
      `m.rtc.notification` as a plaintext (sticky) event in encrypted rooms, so this is expected.

## 7. Open questions for M2

Test against the clients users actually run, and record:

1. Does the first key arrive before the bridge starts publishing? How long is the gap?
2. Does the sender know the newly created ghost device immediately (`/keys/query` propagation), or
   is the first key dropped as "unknown device"?
3. Rotation on join (inside/outside the 10 s grace) and on leave; index values observed; wrap not
   practical to test in a call, cover in unit tests.
4. Element Call modes: Compatibility vs `matrix_2_0` / sticky `m.rtc.member`.
5. Element X Android (WebView/JS) and Element X iOS native call stack. The iOS native path was not
   traced in this pass; verify it uses the same event and trust rules, and whether its LiveKit
   provider uses HKDF rather than the SDK default PBKDF2.
6. Whether any client applies MSC4153 ("cross-signed only") to to-device key events.
7. Multiple owner devices in one call; keys must be tracked per device, not per user.

## 8. References

- MSC4143 (MatrixRTC): "Distributing keys", "Rotating keys", "Relation between MatrixRTC encryption
  and room encryption", "Unstable prefix", "Implementation differences in the reference
  implementation".
- MSC4195 (LiveKit transport): "LiveKit participant identities", "End-to-end encryption", appendix
  (16-byte generic secret).
- MSC4190 (appservice device management), MSC4326 (device masquerading), MSC3202 (transaction
  extensions), MSC4153 (exclude non-cross-signed devices), MSC4354 (sticky events).
- RFC 9605 (SFrame), for contrast only.
- Code references are inline above; all paths are relative to the pinned repositories/versions
  listed at the top.
