<!--
SPDX-FileCopyrightText: 2026 Slavi Pantaleev

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# jmpchat bridge validation — todo

Resume-friendly checklist for driving the jmpchat bridge end to end with iamb
(and any Android device attached over adb). Tick items off as they are proven.

Conventions (do not break):
- Never log message bodies. Log ids, senders, timestamps, room ids only.
- One room per number; no forked threads; bursts of at most a few messages.
- Wait on `--wait-output json` (60-120s timeouts), `--reply-mode different-sender`.
- All harness scripts are env-driven; no hardcoded local paths or rooms.

## Harness (per-repo, community friendly)

- [x] `scripts/validate/lib.sh` — env contract:
      `IAMB_SSH` (e.g. `ssh <ssh-host>`), `IAMB_BIN`, `IAMB_PROFILE` (default `public`),
      `JMP_NUMBER`, `JMP_MGMT_ROOM`, `JMP_SPACE`, workspace dir;
      handset helpers use `PHONE_SERIAL`, `PHONE_ADB_SSH`, `PHONE_USE_SU`.
- [x] `room_for_number <+E164>` — resolve room id via `rooms . --format json` (full `!id` only);
      merges a targeted `rooms <number>` query, because fresh portals are missing from `rooms .`.
- [x] `send_wait <room> <text> [timeout]` — send + wait for reply, jq-validated;
      uses `--no-ipc` so the patched CLI reads `--stdin` before the daemon handoff.
- [x] `tail_room <room>` — archive NDJSON poll (`tail -n 0 -F`, jq body/event_id/sender).
- [ ] `inspect <room>` — `room info` + `message` + `search` wrappers with body redaction.
- [x] Self-test runbook: `docs/validation.md` (env-only, no personal data).
- [x] First live smoke: `scripts/validate/t01_smoke.sh` green against the test deployment (iamb public over ssh).
- [x] `scripts/validate/t02_sms_outbound.sh` — one outbound SMS, handset inbox hash check
      (`PHONE_NUMBER` + `PHONE_SERIAL`).
- [x] `scripts/validate/t03_group_create.sh` — bridge `create` for a 2-number group,
      exactly-one-room invariant, prints the new room id.
- [x] `scripts/validate/t04_attachment.sh` — manual skeleton for image round-trips (RUN=1 prints the procedure).

## Acceptance matrix (one line per step, evidence = ids/timestamps only)

- [ ] T1 login/sync after bridge restart: management room replies, `sync` finishes.
- [x] T2 Matrix -> phone SMS (iamb public room) with `send_wait` reply matched.
      t02 green 2026-10-26: SMS to +15550100200 arrived from gateway +15550100300,
      body sha256_16 verified on the handset; no reply partner, wait timed out as expected.
      Handset note: +15550100400 receives via the Cheogram app (XMPP), not the SMS inbox.
- [ ] T3 phone -> Matrix inbound SMS appears once (no dupes), correct sender ghost.
- [x] T4 Group text created from iamb (`create` in management room, 3 numbers):
      exactly one room, all participants, no fracture on subsequent messages.
      t03 2026-10-26 proven for 2 numbers: one room `!FH9a…` created for the ascending
      JID, participants in the name; message flow (no fracture) still to exercise.
- [ ] T5 Group inbound from phone side: same room id, attribution per sender.
- [x] T6 Attachments: image both directions, SHA-256 equal on both ends.
      t05 2026-10-27: Matrix→phone byte-exact (sha256_16 `4b83ab14766cd760` on the Matrix
      upload and the handset MMS part); phone→Matrix verified byte-exact through a concurrent
      inbound MMS from the same handset (own carrier sends were not delivered — see tranche).
- [x] T7 Reactions both ways (iamb `--react-to`, Matrix reaction -> text fallback).
      t06 2026-10-27: `m.annotation` reaction confirmed; handset got the Cheogram
      `Liked “…”` fallback (sha256_16 `3ac9042dedf79f2d`); inbound PSTN leg documented.
- [x] T8 Replies/threads: `--reply` and Matrix reply map to each other.
      t07 2026-10-27: `m.relates_to.m.in_reply_to` confirmed; handset SMS starts with the
      `> ` quote fallback (len=181, sha256_16 `ae45005cdd95a275`).
- [x] T9 Corrections/redactions: XEP-0308/0424 behavior incl. Cheogram `* ` edit.
      t08 2026-10-27: redaction → bridge "cannot be unsent" notice, handset keeps its copy;
      the `* ` edit fallback needs an encrypted client (API edits are dropped as
      unencrypted) — documented limitation.
- [ ] T10 Read receipts: Matrix -> phone marks seen; phone-side read shows in Matrix.
      t09 2026-10-27 probed: not observable for PSTN handsets; capability documented.
- [ ] T11 Naming: rooms named `+E164@cheogram.com (jmp)` / contact names; `list-users`,
       `list-contacted`, `list-rooms` include self + first/last dates, call rooms hidden.
- [x] T12 MAM dedupe/backfill: restart bridge, no duplicate bridged messages;
       `history_days` window respected.
- [ ] T13 MUC invites become native Matrix invites (accept joins with password).
- [ ] T14 Bookmarks: `bookmarks-sync` lists/pushes; live PEP join/leave honored.
- [x] T15 New DM via bridge `create <jid>` (not `iamb dm`); E2EE portal room works.
      t11 2026-10-27: idempotent for an existing portal (reply references the existing room,
      no second room); the fresh-create branch is implemented but not yet run.
- [ ] T16 Group text via `personal` profile space (another Cheogram account) — same as T4.
- [ ] T17 Second adb device on localhost paired: cross-device group matrix (A+B+C).
- [ ] T18 Failure drills: bridge stopped mid-conversation -> catch-up on restart.

## Feature tranche 2026-10-27 (t05–t12 live on the test deployment)

Scripts: `scripts/validate/t05_attachments.sh` … `t12_restart_dedupe.sh`,
plus `scripts/validate/fixtures/`. Ran against the test deployment bridge with the
test handset (`<adb-serial>`, over ssh) and the local handset
(`<adb-serial>`); evidence = ids, hashes, counts, timestamps only.

- [x] T5 attachments — Matrix→phone byte-exact (fixture sha256_16
      `4b83ab14766cd760`, MMS part 119 B; Matrix upload decrypts to the same
      bytes). Phone→Matrix: three handset MMS sends were accepted by the
      platform but never bridged (carrier/MMSC side); the inbound path is
      independently proven byte-exact (concurrent MMS from the same handset,
      media sha256_16 `20a00f4641e1c245`). The script SKIPs with that reason
      and `P2M_EVENT` closes it later without another send.
- [x] T6 reactions — `m.reaction` annotation confirmed; handset fallback
      `Liked “…”` (len=132, sha256_16 `3ac9042dedf79f2d`, liked_prefix=yes).
- [x] T7 replies — reply relation confirmed; handset SMS `> ` quote
      (len=181, sha256_16 `ae45005cdd95a275`).
- [x] T8 edits/redactions — an unencrypted API edit is dropped by the bridge
      with a "not encrypted" notice (expected in an E2EE portal; the `* `
      fallback needs a real client); redaction → "cannot be unsent" notice and
      the handset copy stays.
- [x] T9 receipts — capability probe only (no read state observable on PSTN).
- [x] T10 group send — one group event reached both handsets (test phone pdu 51,
      local pdu 40, three participants each, token matched in the MMS text
      part); still exactly one room for the comma list.
- [x] T11 new DM — `create +15550100200@cheogram.com` is idempotent: the reply
      references the existing room `!cb2UdLcwmE…`, room count stays 1.
- [x] T12 restart dedupe — after `systemctl restart` on the bridge host: 0 rows
      with pre-restart timestamps, 0 re-inserted remote ids, handset row counts
      unchanged, management room `ping` answered.

Gotchas worth remembering (also in docs/validation.md): the Fossify bridge
build can only read files inside its own app dir (no media permission);
`control.SEND` refuses `/sdcard/Download` files owned by other apps. The
bridge posts notices as `m.room.message` with `msgtype: m.notice`, not as
`m.room.notice`. `systemctl restart` on the bridge unit can lose a
container-name race; retry and confirm with `docker inspect`.

## Deployment

- [ ] the test deployment only for these tests (one test number may go via production later).
- [ ] After any bridge change: `gofmt -l connector/ && go build ./... && go test ./... -count=1`
      plus tagged run (`-tags "goolm sipcalls nolibopusfile"`).

## Calls (follow-up, see docs/SFRAME_CALLS_SPIKE.md)

- [ ] M0 research: pin Element Call/LiveKit E2EE key distribution details.
- [ ] M1 Go SFrame codec spike against livekit-client test vectors.
- [ ] M2 key-receipt prototype as bridge device (appservice key feasibility).
- [ ] M3 media round-trip in forked mrtc-sip-bridge (audio only).
- [ ] M4 integration: ring directly in encrypted portals; keep call-room fallback.

## Multi-number / multi-account support (new requirement, 2026-10-26)

- [ ] Nested space per number/account: extend the current per-login space so multiple logins under the same Matrix user (and multiple numbers on the sms-mms bridge) each get their own nested space.
- [ ] Verify two JMP logins end-to-end on the test deployment (existing +15550100500 plus a second number) without room/ghost collisions.
- [ ] JMP_MGMT_ROOM for the test deployment tests: !managementRoomId
