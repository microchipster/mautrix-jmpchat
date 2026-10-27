# Bridge validation harness

Small, env-driven scripts that drive the jmpchat bridge through an
`iamb` daemon (over SSH) and, where a handset is needed, read SMS off an
adb-attached device. They are meant for sanctioned low-volume testing of a
personal JMP.chat number, not for load or marketing use.

Everything runs against live infrastructure: a wrong variable or a duplicate
run sends real SMS. Read this page before running anything.

## Scripts

| Script | What it proves | Sends SMS? |
|---|---|---|
| `scripts/validate/lib.sh` | shared helpers and env contract (source only) | no |
| `scripts/validate/t01_smoke.sh` | iamb daemon reachable, JMP number resolves to a bridged room with the bridge marker | no |
| `scripts/validate/t02_sms_outbound.sh` | Matrix → phone SMS: send one token-carrying message, find it in the handset inbox, compare body hashes | yes, one message |
| `scripts/validate/t03_group_create.sh` | group portal creation via the bridge `create` command; exactly one new room for the ascending comma JID | no (portal only) |
| `scripts/validate/t04_attachment.sh` | image round-trip Matrix ↔ phone with sha256 comparison | skeleton only; manual |
| `scripts/validate/t05_attachments.sh` | attachment round-trip: iamb `--attach` → handset MMS part hash, and handset MMS → bridged m.image with media hash check | yes, one attachment + one handset MMS |
| `scripts/validate/t06_reactions.sh` | Matrix reaction → phone, with the Cheogram `Liked “…”` SMS fallback observation; inbound reaction leg documented (PSTN cannot carry XEP-0444) | maybe one fallback SMS |
| `scripts/validate/t07_replies.sh` | iamb `--reply` → Matrix reply relation, and the `> quoted` fallback SMS on the handset | yes, one reply |
| `scripts/validate/t08_edits_redactions.sh` | Matrix edit → `* ` fallback expectation and the bridge's unencrypted-edit drop; Matrix redaction → "cannot be unsent" notice | yes, one target SMS |
| `scripts/validate/t09_receipts.sh` | read-receipt capability probe (PSTN handsets expose no read state; SKIPs when nothing is observable) | no |
| `scripts/validate/t10_group_send.sh` | group portal message reaches every handset exactly once, no forked room, participant list evidence | yes, one group message |
| `scripts/validate/t11_new_dm.sh` | bridge `create` for a DM: idempotent when the portal exists (no duplicate room), creates exactly one otherwise | no |
| `scripts/validate/t12_restart_dedupe.sh` | restart the bridge; no replayed MAM rows, no re-bridged ids, no new handset rows, management room answers `ping` | no |
| `scripts/validate/fixtures/` | deterministic 64x64 PNG used by the attachment test, with pinned sha256 and `generate.sh` | no |

All scripts are idempotent where possible and read-only with respect to the
handsets (SMS/MMS rows are queried, never inserted, updated or deleted by the
harness itself; message sends go through the sanctioned control surface or a
normal Matrix send).

## Environment variables

Required by `lib.sh` consumers as noted: the iamb helpers fail with a clear
error when `IAMB_SSH` is unset. The rest are optional with sensible defaults.

| Variable | Used by | Meaning | Default |
|---|---|---|---|
| `JMP_NUMBER` | t01, t03, t04 | JMP.chat test number, E.164 (`+15551234567`) | required |
| `IAMB_SSH` | all | SSH host running the `iamb` daemon | required |
| `IAMB_BIN` | all | iamb binary path on that host | `~/.cargo/bin/iamb` |
| `IAMB_PROFILE` | all | iamb profile name | `public` |
| `IAMB_ARCHIVE_DIR` | archive reads | iamb event archive directory on `IAMB_SSH` | `~/.local/share/iamb/profiles/$IAMB_PROFILE/events` |
| `IAMB_SESSION_JSON` | Matrix API reads | iamb session file on `IAMB_SSH`; the token inside is used only on that host | `~/.local/share/iamb/profiles/$IAMB_PROFILE/session.json` |
| `JMP_MGMT_ROOM` | t03, t11, t12 | bridge management room (`!...`), the bot DM that takes `create` and `ping` | required by t03, t11, t12 |
| `VALIDATE_TIMEOUT` | all | default wait timeout, seconds | `90` |
| `PHONE_SERIAL` | t02, t05–t12 | adb serial of the handset to read | required by t02/t05–t10 |
| `PHONE_NUMBER` | t02, t05–t12 | number the SMS is addressed to; the harness resolves its portal room | required by t02, t05–t09, t12 |
| `PHONE_DB` | handset DB reads | mmssms.db path on the handset | AOSP telephony provider path |
| `PHONE_ADB_SSH` | all handset helpers | SSH host that has the handset attached; unset = local adb | unset |
| `PHONE_A_ADB_SSH` / `PHONE_B_ADB_SSH` | t10 | per-handset SSH hops (the global `PHONE_ADB_SSH` is not used by t10) | unset |
| `PHONE_USE_SU` | all handset helpers | set `1` to run device commands via `su` | `0` |
| `PHONE_CONTROL_PKG` | t05, t06 | Fossify Messages bridge build to control | `org.fossify.messages.bridge.debug` |
| `SEND_WAIT_TIMEOUT` | t02, t05, t07, t08, t10 | seconds to wait for a reply after the send | `25` |
| `PHONE_SMS_TIMEOUT` | t02, t07, t08, t10 | seconds to poll the handset inbox | `120`–`180` per test |
| `PHONE_MMS_TIMEOUT` / `PHONE_MMS_POLL` | t05 | MMS polling window and interval | `180` / `8` |
| `MATRIX_WAIT_TIMEOUT` | t05 | seconds to poll for a bridged inbound event | `180` |
| `P2M_ATTEMPTS` | t05 | how many real handset MMS sends the phone → Matrix leg may make | `1` |
| `M2P_EVENT` / `P2M_EVENT` | t05 | verify an existing Matrix/image event instead of sending (no new send) | unset |
| `REACT_TARGET_EVENT` / `REACT_EVENT` | t06 | reaction target / verify an existing reaction event | unset (newest phone message) |
| `REPLY_TARGET_EVENT` / `REPLY_EVENT` / `REPLY_TOKEN` | t07 | reply target / verify an existing reply and its token | unset (newest phone message) |
| `ALLOW_PLAINTEXT_EDIT` | t08 | accept that the API-sent edit is unencrypted in an E2EE portal | `0` |
| `SKIP_EDIT` / `EDIT_TARGET_EVENT` / `REDACT_TARGET_EVENT` | t08 | skip the edit leg / choose the target events | unset (fresh target) |
| `JMP_BRIDGE_SSH` | t09, t12 | host that runs the bridge container; unset = those probes SKIP | unset |
| `JMP_BRIDGE_SERVICE` / `JMP_BRIDGE_CONTAINER` | t12 | systemd unit and container name | `matrix-mautrix-jmpchat` |
| `JMP_BRIDGE_PG_CONTAINER` / `JMP_BRIDGE_PG_USER` / `JMP_BRIDGE_PG_DB` | t12 | bridge database access for the dedupe diff | `matrix-postgres` / `matrix` / `matrix_bridge_mautrix_jmpchat` |
| `BRIDGE_SETTLE` / `BRIDGE_RESTART_TIMEOUT` | t12 | settle time and liveness timeout after a restart | `30` / `90` |
| `GROUP_PHONES` | t10 | comma-joined E.164 list of the group portal | required by t10 |
| `PHONE_A_SERIAL` / `PHONE_B_SERIAL` | t10 | the two handsets that must receive the group message | required by t10 |
| `GROUP_PHONE_A` / `GROUP_PHONE_B` | t03 | two same-length E.164 numbers for the group JID | required by t03 |
| `TARGET_PHONE` / `TARGET_JID` | t11 | DM target for the bridge `create` command | required by t11 |
| `RUN` | t04 | set `1` to print the manual attachment procedure | `0` |

The SMS helpers accept a handset attached to another host with
`PHONE_ADB_SSH=<ssh-host>` and a device serial; the localhost phone just needs
`PHONE_SERIAL`.
Both test handsets read their inbox as the adb `shell` user today, so
`PHONE_USE_SU` is only a fallback. On a device where `content query` fails
with permissions, try `PHONE_USE_SU=1`. t10 routes each handset separately
(`PHONE_A_ADB_SSH` / `PHONE_B_ADB_SSH`) because one hop per device is needed
when only some handsets hang off an SSH host.

## Running

```bash
cd mautrix-jmpchat

# T01 — smoke, no messages
JMP_NUMBER=+15551234567 scripts/validate/t01_smoke.sh

# T02 — one outbound SMS to a handset
PHONE_NUMBER=+15105550003 \
PHONE_SERIAL=<adb-serial> \
scripts/validate/t02_sms_outbound.sh

# T02 variant when the handset hangs off an SSH host
PHONE_NUMBER=+15105550003 \
PHONE_ADB_SSH=<ssh-host> \
PHONE_SERIAL=<adb-serial> \
scripts/validate/t02_sms_outbound.sh

# T03 — group portal creation (ascending JIDs, one room expected)
JMP_NUMBER=+15551234567 \
JMP_MGMT_ROOM='!managementRoomId' \
GROUP_PHONE_A=+15105550001 \
GROUP_PHONE_B=+15105550002 \
scripts/validate/t03_group_create.sh

# T04 — attachment skeleton (dry run / instructions only)
scripts/validate/t04_attachment.sh
RUN=1 scripts/validate/t04_attachment.sh

# T05 — attachment round-trip (one Matrix attachment + one handset MMS)
JMP_NUMBER=+15551234567 \
PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> [PHONE_ADB_SSH=<ssh-host>] \
scripts/validate/t05_attachments.sh

# T05 verification-only runs (no new send), once an event id exists
M2P_EVENT='$matrixImageEvent' P2M_EVENT='$bridgedInboundImageEvent' \
PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
scripts/validate/t05_attachments.sh

# T06 — reactions (Matrix -> phone, fallback observation)
JMP_NUMBER=+15551234567 PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
scripts/validate/t06_reactions.sh

# T07 — replies (Matrix -> phone)
JMP_NUMBER=+15551234567 PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
scripts/validate/t07_replies.sh

# T08 — edits/redactions (the edit leg needs ALLOW_PLAINTEXT_EDIT=1)
JMP_NUMBER=+15551234567 PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
JMP_BRIDGE_SSH=<bridge-host> ALLOW_PLAINTEXT_EDIT=1 \
scripts/validate/t08_edits_redactions.sh

# T09 — read-receipt capability probe (read-only)
JMP_NUMBER=+15551234567 PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
[JMP_BRIDGE_SSH=<bridge-host>] \
scripts/validate/t09_receipts.sh

# T10 — group send to two handsets (one group message)
JMP_NUMBER=+15551234567 \
GROUP_PHONES='+15550000001,+15550000002' \
PHONE_A_SERIAL=<serial-a> [PHONE_A_ADB_SSH=<host-a>] \
PHONE_B_SERIAL=<serial-b> [PHONE_B_ADB_SSH=<host-b>] \
scripts/validate/t10_group_send.sh

# T11 — DM create and idempotency (no SMS)
JMP_NUMBER=+15551234567 JMP_MGMT_ROOM='!managementRoomId' \
TARGET_PHONE=+15557654321 \
scripts/validate/t11_new_dm.sh

# T12 — restart dedupe (restarts the bridge!)
JMP_NUMBER=+15551234567 PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
JMP_MGMT_ROOM='!managementRoomId' JMP_BRIDGE_SSH=<bridge-host> \
scripts/validate/t12_restart_dedupe.sh
```

`t02` needs a portal room for `PHONE_NUMBER`; create it once by sending
`create <number>@cheogram.com` in the management room (no SMS is sent by that
command). Sends wait for a reply; a receive-only handset never replies, so a
wait timeout is expected and recorded, while the pass/fail decision comes from
the handset inbox. Because the SMS may arrive from a gateway number, the inbox
read is filtered by the time window of the run and matched by the unique token.
`t03` prints the new room id on stdout and accepts one brand-new pending invite
automatically when the bridge invites instead of joining the user.

`t05` sends the fixture image from Matrix with `iamb --attach`, hashes the
image part in the handset MMS database, then asks the handset (Fossify
Messages bridge build `control.SEND`) to send the same fixture as an MMS back
to the JMP number and hashes the decrypted media of the bridged `m.image`
event. Byte-exactness is reported per hop; gateways may re-encode, so only a
missing image fails the test. The file handed to `control.SEND` must be
readable by the app: the script pushes it to the app's own external files dir
(`/sdcard/Android/data/<pkg>/files/`) because the app has no media read
permission.

`t07`/`t08` match the expected SMS on the handset by a unique token, so
concurrent traffic on the same handset cannot be mistaken for the test's own
message.

`t12` reads the bridge database (read-only) to diff `message` rows across the
restart: rows inserted after the restart must not carry pre-restart remote
timestamps (a MAM replay signal) and must not re-insert known remote ids.

## Evidence rules

* Never log message bodies. The harness prints ids, senders, timestamps,
  lengths and sha256 prefixes only.
* `t02` asserts that the body hash read from the handset equals the hash of
  the text that was sent, and prints only the hash.
* `t03` prints only the new room id; display names contain phone numbers and
  stay out of the logs.
* Keep evidence per run: room ids, event ids, handset row dates, sha256_16
  values. That is enough to reconstruct what happened without the bodies.

## Caveats and known limitations

* **Fuzzy room names hang automation.** Always pass full `!room_id` values;
  a non-exact `--room` match opens an interactive prompt that never returns.
* **Some joined rooms are missing from `rooms .`.** Observed: the bridge
  management DM (`jmpchat bridge bot`) and freshly created portals are
  resolvable with `room info` or a targeted `rooms <number>` query but do not
  appear in the `rooms . --format json` snapshot. The harness resolvers
  (`room_for_number`, `room_count_for_number`) therefore merge the snapshot
  with a targeted query; do not rely on directory listings alone for
  management rooms.
* **Patched iamb CLI: `--stdin` is refused on the IPC fast path.** The CLI
  reads piped bodies only on the local execution path; forwarded commands
  fail with `--stdin must be read by the invoking CLI process before send
  execution`. The harness send helpers pass `--no-ipc` so the CLI reads stdin
  and then hands the prepared send to the running daemon (via the store-lock
  fallback). A CLI-side fix would read stdin for `Command::Send` before IPC
  forwarding.
* **E2EE on `dm`-created rooms is unverified.** Rooms created with `iamb dm`
  enable encryption before the bridge is involved; use the bridge's `create`
  command instead and treat `dm` portals as experimental.
* **Async room creation.** `t03` polls the merged room listing and may need
  to accept an invite before the new group portal becomes visible.
* **Group room names may drop the domain.** A group portal can be named
  `+15105550001,+15105550002 (jmp)` rather than `<jid>@cheogram.com`; `t03`
  therefore counts and matches on the comma-joined number list, not the JID.
* **Handsets: XMPP-addressed numbers vs PSTN numbers.** A number that is a
  JMP/Cheogram account (e.g. one logged into a Cheogram app) receives in that
  app, not through `content://sms`, so the `t02` SMS-inbox check only applies
  to plain PSTN handsets. Beware that a handset logged into the *same XMPP
  account as the bridge* will show the bridge's outgoing messages as carbon
  copies; a carbon is not delivery proof. `t02` therefore requires a body
  hash match in the SMS provider (seen from a Cheogram gateway number).
* **Group SMS is beta** (bridge and carrier side). Keep groups small.
* **Matrix reactions become `Liked “…”` SMS.** For cheogram.com targets the
  bridge sends a XEP-0444 reaction set plus a body fallback; the Cheogram
  gateway renders that on a PSTN handset as a text message starting with
  `Liked “` (the quoted text is the reacted-to message's link or text), not as
  the emoji glyph. `t06` records the fallback hash and the `Liked ` prefix.
* **Reply fallbacks survive as `> ` quotes.** `t07` verified that the handset
  SMS body starts with the XEP-0461 quote marker before the reply text.
* **Edits in E2EE portals need an encrypted sender.** iamb has no edit
  command, and a Matrix-API-sent edit is unencrypted; the bridge correctly
  drops it in an encryption-required portal and posts an
  `m.room.message` with `msgtype: m.notice` ("your message was not bridged:
  the message is not encrypted"). `t08` therefore documents the `* ` fallback
  expectation from `connector/xmpp_corrections.go` and needs a real encrypted
  client to exercise it end to end.
* **Redactions produce a notice, not an unsend.** Redacting a bridged message
  makes the bridge post an `m.room.message` with `msgtype: m.notice` ("SMS/MMS
  messages cannot be unsent…"); the handset keeps its copy. Match on
  `msgtype == "m.notice"`, not on the event type.
* **Handset-originated MMS can be slow or silently lost.** Sends accepted by
  the platform (`MmsServiceBroker sendMessage`) have been observed to never
  reach the bridged room within minutes, while other sends from the same
  handset arrive in ~90s. The phone → Matrix leg of `t05` therefore SKIPs with
  that reason instead of failing, prints the handset pdu state, and accepts
  `P2M_EVENT` so a late arrival can be verified without another send.
* **Parallel runs on the same handset/room.** Matching "the newest message"
  is unreliable when another test run is active; `t05`–`t10` assert on unique
  tokens and hashes, and `t12` watches the bridge database instead of live
  room event lists.
* **Bridge restart races with docker's name cleanup.** `systemctl restart`
  on the bridge unit can fail transiently with a container-name conflict;
  `bridge_restart` retries once and `bridge_wait_running` is the authority on
  whether the bridge actually came back.
* **Terms of service.** JMP.chat prohibits automation, marketing and
  campaigns. This harness exists for sanctioned, low-volume testing of a
  personal number; do not turn it into a bulk messaging system.
* **`iamb listen` is not used.** Reply loops would double-login; the harness
  polls the archive and sends over IPC instead.
* **`t04` is superseded by `t05`.** MMS payloads are not exposed through the
  `content://sms` provider, but root adb can read the `part` table and hash
  the part files in place; `t05` does that in both directions. `t04` stays as
  the manual-procedure fallback for handsets without database access.
