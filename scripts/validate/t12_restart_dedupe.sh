#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T12 — MAM dedupe and restart behaviour.
#
# Restarts the bridge service on JMP_BRIDGE_SSH, waits for the container and
# a fresh XMPP health-check line, then proves the reconnect did not replay
# history and did not re-send anything to the phone:
#   * no message row was inserted after the restart with a remote timestamp
#     older than the restart (that is what a MAM replay would look like);
#   * no already-known remote message id was inserted again (bridge `message`
#     table diff, see scripts/validate/docs in docs/validation.md);
#   * the handset gained no SMS/MMS rows;
#   * the management room answers a `ping` again.
#
# Portal-room event lists change with live traffic (other clients may be
# active), so they are recorded as informational evidence, not asserted on.
#
# The test SKIPs when JMP_BRIDGE_SSH is unset: the harness must not restart
# infrastructure it cannot reach.
#
# Usage:
#   JMP_NUMBER=+15551234567 PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
#   JMP_MGMT_ROOM='!managementRoomId' JMP_BRIDGE_SSH=<ssh-host> \
#   scripts/validate/t12_restart_dedupe.sh
#
# Optional:
#   BRIDGE_SETTLE           seconds to let the bridge reconnect (default 30)
#   BRIDGE_RESTART_TIMEOUT  seconds to wait for liveness (default 90)
#   JMP_BRIDGE_PG_CONTAINER / JMP_BRIDGE_PG_USER / JMP_BRIDGE_PG_DB
#                           database access for the dedupe diff
#   PHONE_* / IAMB_*        see lib.sh

set -euo pipefail
LIB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$LIB_DIR/lib.sh"

: "${BRIDGE_SETTLE:=30}"

require_jmp_env
require_env PHONE_NUMBER PHONE_SERIAL JMP_MGMT_ROOM

if [ -z "${JMP_BRIDGE_SSH:-}" ]; then
	skip "JMP_BRIDGE_SSH is unset; refusing to restart infrastructure the harness does not control"
fi

portal=$(room_for_number "$PHONE_NUMBER")
[ -n "$portal" ] || die "no portal room for $PHONE_NUMBER"
self_room=$(room_for_number "$JMP_NUMBER")
log "portal room: $portal"

# --- baseline -------------------------------------------------------------
base_rowid=$(bridge_db_query "select coalesce(max(rowid), 0) from message" | tr -d '\r')
base_portal_rows=$(bridge_db_query "select count(*) from message m join portal p on (m.bridge_id, m.room_id, m.room_receiver) = (p.bridge_id, p.id, p.receiver) where p.mxid = '$portal'" | tr -d '\r')
base_ids_hash=$(room_event_ids_hash "$portal" 20)
base_newest=$(room_event_ids "$portal" 1)
base_phone_sms=$(phone_db_query "$PHONE_SERIAL" "select count(*) from sms" | tr -d '\r')
base_phone_pdu=$(phone_db_query "$PHONE_SERIAL" "select count(*) from pdu" | tr -d '\r')
log "baseline: message rowid max=$base_rowid portal rows=$base_portal_rows ids_hash=$base_ids_hash newest=$base_newest"
log "baseline: handset sms_rows=$base_phone_sms pdu_rows=$base_phone_pdu"

restart_epoch=$(date +%s)
restart_ms=$(( restart_epoch * 1000 ))

# --- restart --------------------------------------------------------------
bridge_restart
bridge_wait_running
if bridge_wait_xmpp "$restart_epoch" >/dev/null; then
	log "bridge logged a fresh XMPP health check"
else
	log "WARNING: no fresh XMPP health-check line within the timeout; continuing to the ping check"
fi
log "letting the bridge settle for ${BRIDGE_SETTLE}s before comparing"
sleep "$BRIDGE_SETTLE"

# --- checks ---------------------------------------------------------------
replayed=$(bridge_db_query "select count(*) from message where rowid > $base_rowid and timestamp < $restart_ms" | tr -d '\r')
log "rows inserted after the restart with pre-restart timestamps (MAM replay signal): $replayed"
[ "${replayed:-1}" -eq 0 ] || die "$replayed message row(s) inserted after the restart carry pre-restart timestamps — likely a replayed MAM/transaction"

dupes=$(bridge_db_query "select count(*) from message m2 where m2.rowid > $base_rowid and exists (select 1 from message m1 where m1.rowid <= $base_rowid and m1.id = m2.id and m1.room_receiver = m2.room_receiver)" | tr -d '\r')
log "already-known remote ids inserted again: $dupes"
[ "${dupes:-1}" -eq 0 ] || die "$dupes remote message id(s) were bridged again after the restart"

after_phone_sms=$(phone_db_query "$PHONE_SERIAL" "select count(*) from sms" | tr -d '\r')
after_phone_pdu=$(phone_db_query "$PHONE_SERIAL" "select count(*) from pdu" | tr -d '\r')
log "handset after restart: sms_rows=$after_phone_sms pdu_rows=$after_phone_pdu"
[ "${after_phone_sms:-0}" -eq "${base_phone_sms:-0}" ] || die "handset SMS rows grew across the restart ($base_phone_sms -> $after_phone_sms); check for re-sent messages"
[ "${after_phone_pdu:-0}" -eq "${base_phone_pdu:-0}" ] || die "handset MMS rows grew across the restart ($base_phone_pdu -> $after_phone_pdu); check for re-sent messages"

errors=$(bridge_log_grep_since 'level=error|"level":"error"| ERR |duplicate key|Failed to insert' "$restart_epoch" || true)
log "bridge error-ish log lines since the restart: ${errors:-0}"
[ "${errors:-0}" -eq 0 ] || die "${errors} error-level bridge log line(s) since the restart; inspect 'docker logs' (dedupe failures show up here)"

assert_room_count "$PHONE_NUMBER" 1

log "checking management-room liveness with 'ping'"
ping_resp=$(management_send_wait "ping" 60 || true)
[ -n "$ping_resp" ] || die "management room returned no JSON after the restart"
printf '%s' "$ping_resp" | wait_expect_success "t12 ping" >/dev/null

after_portal_rows=$(bridge_db_query "select count(*) from message m join portal p on (m.bridge_id, m.room_id, m.room_receiver) = (p.bridge_id, p.id, p.receiver) where p.mxid = '$portal'" | tr -d '\r')
after_ids_hash=$(room_event_ids_hash "$portal" 20)
log "informational: portal message rows $base_portal_rows -> $after_portal_rows; newest-20 ids hash $base_ids_hash -> $after_ids_hash"
[ -n "$self_room" ] && log "informational: self room $self_room ids hash $(room_event_ids_hash "$self_room" 5)"

log "T12 restart dedupe OK (restart_epoch=$restart_epoch; no replayed rows, no duplicate ids, no new handset rows, ping answered)"
pass "T12"
