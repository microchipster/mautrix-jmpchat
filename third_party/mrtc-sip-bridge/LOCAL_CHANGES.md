<!--
SPDX-FileCopyrightText: 2026 Slavi Pantaleev

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Local changes

This directory is a vendored copy of [`github.com/williamkray/mrtc-sip-bridge`](https://github.com/williamkray/mrtc-sip-bridge) (v0.3.1 at the time of vendoring), used through a `replace` directive in `go.mod` because the bridge needs a few changes that upstream does not have yet.

When bumping the module, rebase these changes onto the new upstream revision. Everything below is intentionally small and self-contained.

## Media (pkg/media)

- `DefaultJitterDelay = 25 ms` / `DefaultJitterMax = 120 ms` (`media.go`), used by `withDefaults()` and set explicitly at the call sites (`pkg/calls/setup.go`, `pkg/calls/outbound.go`): latency-first playout, since the PSTN leg is jitter-buffered upstream.
- Jitter drop ratchet softened to `latencySlack = 3` / `latencyPops = 25` (`jitter.go`): a growing sender-clock mismatch drops one frame at a time instead of chopping.
- Per-second `media: trace` DEBUG line (`trace.go`): jitter depths, sip/rtc counters, concealed frames, publish write duration, per-frame buffer residence (`s2r_age_ms`/`r2s_age_ms`) — the tool that located the growing-delay and silence bugs.
- `JMPCHAT_MEDIA_DUMP` env-gated PCM capture (`dump.go`): writes up to 60 s per direction per call as WAVs into the given directory, for offline audio-quality analysis. Off unless the environment variable is set.

## Crypto / rooms (pkg/core, pkg/calls, pkg/rtc)

- `pkg/core/framecrypto.go`: `FrameCrypto` seam (`Sealer()`/`Opener(identity)`) plus `core.CallRoom.Crypto`.
- `calls.Config.DeviceID` (default `SIPCALL`): one stable Matrix device for every ghost call membership and its call-encryption keys.
- The encrypted-room refusal (D18) only triggers when `room.Encrypted && room.Crypto == nil`, so hosts that can distribute SFrame keys may ring encrypted rooms.
- `pkg/rtc/livekit.go`: media is sealed with the room's SFrame keys when a `FrameCrypto` is present, the track is published with `Encryption_GCM`, incoming SIF frames are dropped and incoming tracks are decrypted per sender identity. The publish capability must stay `opus/48000/2` — a mono capability makes `setRemoteDescription` fail with `codec is not supported by remote` and the bridge publishes nothing.

## Call UX (pkg/calls)

- `core.CallHost.NotifyManagement` + `fail`/`failOutbound` management notices: missed, busy, rejected and otherwise silent call ends are reported to the owner's management room (rate-limited by the host).
- The ring is accompanied by an urgent room message ("Incoming call from …") so clients without MatrixRTC notification support still get a push and an unread; it is marked read once the call is answered (missed calls keep it unread on purpose).

## SIP registration (pkg/sipua, pkg/sipaccount)

- `sipua.Config.PinContact` (set by the connector whenever `calls.external_ip` is configured): the REGISTER Contact is pinned to `external_ip:bind_port` and diago's rport/received rewrite is skipped (`Register`/`Refresh` call `Qualify`). Docker port publishing makes the NAT-observed source port an unpublished ephemeral host port, so following it would advertise an unreachable Contact and break inbound calls. Without an explicit `external_ip` (STUN or bind-IP fallback) the upstream NAT-following behavior is kept.
- `answerAckTimeout = 10 s` (`pkg/calls/state.go`, armed in `doAnswer`): once the SIP 200 OK is issued, the caller must ACK it within that window. When the caller's INVITE transaction had already expired, no ACK ever comes; the call now fails after 10 s with "Missed call (could not connect audio)" instead of leaving the owner in dead air until the SIP transaction expires (Timer H, 64*T1 ≈ 32 s).
