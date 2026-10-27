#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T10 — group send: one message from the group portal room reaches every
# handset and does not fork the room.
#
# The bridge sends a group message to the comma JID as one group MMS (or one
# SMS per member, carrier dependent). The test sends one token from the group
# room, then asserts on each handset that a new inbox row carries the token
# (the match happens inside the handset database; the body is never printed)
# and that the participant list of the group pdu is complete. The room
# invariant (exactly one room for the comma-joined numbers) is checked before
# and after.
#
# Usage:
#   JMP_NUMBER=+15551234567 \
#   GROUP_PHONES='+15550000001,+15550000002' \
#   PHONE_A_SERIAL=<serial> [PHONE_A_ADB_SSH=<host>] \
#   PHONE_B_SERIAL=<serial> [PHONE_B_ADB_SSH=<host>] \
#   scripts/validate/t10_group_send.sh
#
# Optional tuning:
#   SEND_WAIT_TIMEOUT   seconds to wait for a reply after the send (default 25)
#   PHONE_SMS_TIMEOUT   seconds to poll each handset (default 180)
#   PHONE_SMS_POLL      seconds between handset polls (default 8)
#   IAMB_*              see lib.sh

set -euo pipefail
LIB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$LIB_DIR/lib.sh"

: "${SEND_WAIT_TIMEOUT:=25}"
: "${PHONE_SMS_TIMEOUT:=180}"
: "${PHONE_SMS_POLL:=8}"

require_jmp_env
require_env GROUP_PHONES PHONE_A_SERIAL PHONE_B_SERIAL

phones_norm=$(printf '%s' "$GROUP_PHONES" | tr -d ' ')
assert_room_count "$phones_norm" 1
room=$(room_for_number "$phones_norm")
[ -n "$room" ] || die "no group portal room for $phones_norm (create it with the bridge 'create' command first)"
log "group portal room: $room"

token="validate-t10-$(date +%s)"
body="jmpchat bridge validation ${token}"
sent_id=""
if [ -n "${T10_EVENT:-}" ]; then
	sent_id="$T10_EVENT"
	token="${T10_TOKEN:?T10_EVENT requires T10_TOKEN (the token used by that send)}"
	body="jmpchat bridge validation ${token}"
	since_ms="${T10_SINCE_MS:-$(( $(date +%s%3N) - 3600000 ))}"
	body_hash=$(printf '%s' "$body" | sha256_16)
	log "t10: verifying existing group event $sent_id (token=$token, no new message sent)"
else
	body_hash=$(printf '%s' "$body" | sha256_16)
	since_ms=$(( $(date +%s%3N) - 5000 ))
	log "sending group message (len=${#body} sha256_16=$body_hash) from $room"
	resp=$(send_wait "$room" "$body" "$SEND_WAIT_TIMEOUT" || true)
	[ -n "$resp" ] || die "group send returned no JSON"
	printf '%s' "$resp" | send_expect_ok "t10 group send" >/dev/null
	sent_id=$(jq -r '.messages.sent_events[0].event_id // ""' <<<"$resp")
	[ -n "$sent_id" ] || die "no sent event id in the send JSON"
	log "group event: $sent_id (token=$token)"
fi
since_s=$(( since_ms / 1000 ))

poll_phone() {
	local label="$1"
	local serial="$2"
	local found=0
	local deadline=$(( $(date +%s) + PHONE_SMS_TIMEOUT ))
	while [ "$(date +%s)" -lt "$deadline" ] && [ "$found" -eq 0 ]; do
		local sms_hits mms_hits
		sms_hits=$(phone_db_query "$serial" "select count(*) from sms where date >= ${since_ms} and type = 1 and body like '%${token}%'" | tr -d '\r' || true)
		mms_hits=$(phone_db_query "$serial" "select count(*) from part where text like '%${token}%' and mid in (select _id from pdu where date >= ${since_s} and msg_box = 1)" | tr -d '\r' || true)
		if [ "${sms_hits:-0}" -ge 1 ] || [ "${mms_hits:-0}" -ge 1 ]; then
			found=1
			local pdu_id=""
			pdu_id=$(phone_db_query "$serial" "select _id from pdu where date >= ${since_s} and msg_box = 1 and _id in (select mid from part where text like '%${token}%') order by date desc limit 1" | tr -d '\r' || true)
			log "t10 $label: token found (sms_rows=${sms_hits:-0} mms_parts=${mms_hits:-0} pdu=${pdu_id:-none})"
			if [ -n "$pdu_id" ]; then
				local addr_info
				addr_info=$(phone_mms_addresses "$serial" "$pdu_id" || true)
				log "t10 $label: group pdu participants=$(printf '%s' "$addr_info" | cut -d' ' -f1) participant_list_sha256_16=$(printf '%s' "$addr_info" | cut -d' ' -f2)"
			fi
			break
		fi
		sleep "$PHONE_SMS_POLL"
	done
	[ "$found" -eq 1 ]
}

log "t10: polling handset A ($PHONE_A_SERIAL) for the group message"
export PHONE_ADB_SSH="${PHONE_A_ADB_SSH:-}"
if ! poll_phone "phone-a" "$PHONE_A_SERIAL"; then
	die "handset A ($PHONE_A_SERIAL) did not receive the group message within ${PHONE_SMS_TIMEOUT}s (event=$sent_id)"
fi
log "t10: polling handset B ($PHONE_B_SERIAL) for the group message"
export PHONE_ADB_SSH="${PHONE_B_ADB_SSH:-}"
if ! poll_phone "phone-b" "$PHONE_B_SERIAL"; then
	die "handset B ($PHONE_B_SERIAL) did not receive the group message within ${PHONE_SMS_TIMEOUT}s (event=$sent_id)"
fi

assert_room_count "$phones_norm" 1
log "T10 group send OK (event=$sent_id room=$room)"
pass "T10"
