<!--
SPDX-FileCopyrightText: 2026 Slavi Pantaleev

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# TODO

Outstanding bridge work only. The completed P0-P3 backlog and the WS1-WS5 workstreams
are archived in [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md), together with the design
considerations and operational lessons. This file keeps what is still open, plus
deferred-by-design notes for revisit.

This backlog targets the Matrix <-> XMPP/Cheogram <-> phone-network gateway path, so
priorities follow the semantics that survive the gateway.

## Validation harness and acceptance runs

Harness: `scripts/validate/lib.sh` (env-driven `IAMB_*`/`JMP_*`/`PHONE_*` contract) plus the runbook in `docs/validation.md`; smoke/tranche scripts `scripts/validate/t01_*.sh` … `t12_*.sh` and `fixtures/`. Evidence is ids, hashes and counts only.

- [ ] `inspect <room>` — `room info` + `message` + `search` wrappers with body redaction.
- [ ] T1 login/sync after bridge restart: management room replies, `sync` finishes.
- [ ] T3 phone -> Matrix inbound SMS appears once (no duplicates), correct sender ghost.
- [ ] T5 group inbound from the phone side: same room id, attribution per sender.
- [ ] T10 read receipts: Matrix -> phone marks seen; phone-side read shows in Matrix; SMS gateway delivery acknowledgements surface as read receipts (PSTN handsets have no read state).
- [ ] T11 naming: rooms named `+E164@cheogram.com (jmp)` / contact names; `list-users`, `list-contacted`, `list-rooms` include self + first/last dates; call rooms hidden.
- [ ] T13 MUC invites become native Matrix invites (accept joins with password) — see the WS2.6 live-validation item below.
- [ ] T14 bookmarks: `bookmarks-sync` lists/pushes; live PEP join/leave honoured.
- [ ] T16 group text via the second (personal) Cheogram profile space — same as T4.
- [ ] T17 second paired device on localhost: cross-device group matrix (A+B+C).
- [ ] T18 failure drill: bridge stopped mid-conversation -> catch-up on restart.
- [ ] Deployment: the test deployment only for these checks; after any bridge change run `gofmt -l connector/ && go build ./... && go test ./... -count=1` plus a tagged run (`-tags "goolm sipcalls nolibopusfile"`).

## Live validation (test deployment)

- [ ] WS2.1: verify join-health behaviour against a live room that rejects joins outright.
- [ ] WS2.3: live validation of cross-client nickname rename races.
- [ ] WS2.6: live validation of the MUC accept/reject invite round trip.
- [ ] WS2.8: live round-trip validation of the group-text model against the JMP gateway before considering any outbound model change.
- [ ] WS4.8: live validation of history retention on the test deployment.
- [ ] WS5.1/WS5.3: live validation with a real SIP account + SFU; retry/backoff when the initial SIP account start fails.
- [ ] WS5.4: publish SIP/RTP ports in the container image/playbook role when calls are enabled.

## Gateway workflows (P0 #1/#4)

- [ ] Interactive gateway-prompt resolution when an identifier is not already a JID (auto-query `jabber:iq:gateway` for foreign gateways).
- [ ] Richer ad-hoc form UX; surface gateway prompts for unknown contacts as guided notices.

## Media and messaging polish

- [ ] P0 #2: thumbnail mapping and per-message name parity for niche Cheogram extensions.
- [ ] P1 #8: richer outbound location rendering.
- [ ] P1 #9: decrypt-free `cid:` resolution decoupled from the carrying message.
- [ ] WS1.6: nick-heuristic parsing of plain-text mentions (`nick:` / whole-word); outbound MUC mention references could carry the nick instead of the bare JID.
- [ ] WS1.2/WS1.3 (phase 2): replay tombstone table against MAM replay resurrection; MUC-moderation authority checks.

## MUC and bookmarks

- [ ] WS2.2: revert the room display name on a failed explicit nick change.
- [ ] WS2.4: history-since-newest-bridged-message refinement for rejoins.
- [ ] WS2.5: optional read-only XEP-0049 legacy bookmark import.
- [ ] WS2.7/P2 #15: disco#info `muc#roominfo` name/description on resync; subject-less clears the topic.

## Identity and contacts

- [ ] WS3.1/P2 #13: group/portal avatars.
- [ ] WS3.2: room-level refusal breaker if a service rate-limits avatar probes.
- [ ] WS3.4: `save-contact` should also write the server vCard; bulk mode.
- [ ] WS3.5: PROPFIND/REPORT addressbook discovery (phase 1 expects a direct `.vcf` export URL), multi-addressbook, tombstones for deliberately removed entries, SRV discovery, `phone.Normalize` parity cases, bulk mode.
- [ ] WS3.6: per-login ghost IDs for multi-login deployments (breaking change; needs a migration design).

## Operations and reliability

- [ ] WS4.1: OpenTelemetry (requires new vendored dependencies; env-only activation design already specified).
- [ ] WS4.2: `login password-server` flow (`host`, `host:port`, `tls://host:port`, `auto`) and the "used account X" notice.
- [ ] WS4.3: `network.allow_private_server_override` SSRF guard (incl. CGNAT; servername always verified against the JID domain).
- [ ] WS4.6: optional RSM `after`-cursor variant instead of the timestamp window.
- [ ] WS4.7: tuning keys for dial timeouts/backoff/MAM interval if operators need them; strip credentials/JIDs from logs.
- [ ] P1 #12: deeper account-level service-outage state detection.

## Multi-number / multi-account support

- [ ] Nested space per number/account: extend the per-login space so multiple logins under the same Matrix user (and multiple numbers on the sms-mms bridge) each get their own nested space.
- [ ] Verify two JMP logins end-to-end on the test deployment without room/ghost collisions (see the WS3.6 caveat in `docs/DEVELOPMENT.md`).

## SFrame/E2EE calls spike (see `docs/SFRAME_CALLS_SPIKE.md`)

- [ ] M0 research: pin Element Call/LiveKit E2EE key distribution details.
- [ ] M1 Go SFrame codec spike against livekit-client test vectors.
- [ ] M2 key-receipt prototype as bridge device (appservice key feasibility).
- [ ] M3 media round-trip in the forked mrtc-sip-bridge (audio only).
- [ ] M4 integration: ring directly in encrypted portals; keep the call-room fallback.

## Deferred by design (revisit only on concrete need)

- WS3.7 presence/last-active bridging — privacy-first; needs a privacy story.
- P3 #17 webxdc — revisit only if gateway traffic starts carrying it.
- P3 #19 advanced file transfer — revisit only if the gateway sends Jingle file transfers.
- P3 #20 registration/onboarding — JMP account creation stays on the JMP website.
- P3 #21 OMEMO — deferred; encrypted payloads degrade to a notice for now (WS1.8).
- P3 #18 raw Jingle — out of scope; calls go through SIP/MatrixRTC (WS5).
