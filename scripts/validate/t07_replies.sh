#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T07 — replies, Matrix -> phone.
#
# Reply to the newest message the phone sent (or REPLY_TARGET_EVENT) with
# iamb's --reply, verify the Matrix event carries the m.in_reply_to relation,
# and check what the handset actually receives. For cheogram.com targets the
# bridge builds an XEP-0461-style fallback (a "> quoted text" block followed
# by the reply body); SMS gateways may strip or reshape it, so the test
# asserts delivery and reports the leading-quote observation explicitly.
#
# Usage:
#   JMP_NUMBER=+15551234567 PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
#   scripts/validate/t07_replies.sh
#
# Optional:
#   REPLY_TARGET_EVENT  reply to this event id instead of the newest phone message
#   REPLY_EVENT         verify this existing reply event instead of sending one
#   REPLY_TOKEN         token used by the existing reply (with REPLY_EVENT)
#   PHONE_SMS_TIMEOUT   seconds to poll the handset inbox (default 90)
#   SEND_WAIT_TIMEOUT   seconds to wait for a reply after the send (default 25)
#   PHONE_* / IAMB_*    see lib.sh

set -euo pipefail
LIB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$LIB_DIR/lib.sh"

: "${PHONE_SMS_TIMEOUT:=90}"
: "${SEND_WAIT_TIMEOUT:=25}"

require_jmp_env
require_env PHONE_NUMBER PHONE_SERIAL

assert_room_count "$PHONE_NUMBER" 1
room=$(room_for_number "$PHONE_NUMBER")
[ -n "$room" ] || die "no portal room for $PHONE_NUMBER"
log "portal room: $room"

if [ -n "${REPLY_TARGET_EVENT:-}" ]; then
	target_event="$REPLY_TARGET_EVENT"
	target_meta=$(event_meta "$room" "$target_event")
else
	target_meta=$(newest_phone_message "$room" 60)
	target_event=$(jq -r '.event_id // empty' <<<"$target_meta")
fi
if [ -z "$target_event" ]; then
	skip "no phone-side message in the recent archive to reply to (run the attachment test first, or set REPLY_TARGET_EVENT)"
fi
log "reply target: $target_meta"

reply_token="${REPLY_TOKEN:-validate-t07-reply-$(date +%s)}"
reply_text="$reply_token bridge validation reply"
sent_id=""
sent_meta=""
if [ -n "${REPLY_EVENT:-}" ]; then
	sent_id="$REPLY_EVENT"
	sent_meta=$(event_meta "$room" "$sent_id")
	sent_ts=$(jq -r '.ts // 0' <<<"$sent_meta")
	since_ms=$(( sent_ts - 2000 ))
	log "t07: verifying existing reply event $sent_id (no new reply sent)"
else
	since_ms=$(date +%s%3N)
	resp=$(send_reply_wait "$room" "$target_event" "$reply_text" "$SEND_WAIT_TIMEOUT" || true)
	[ -n "$resp" ] || die "reply send returned no JSON"
	printf '%s' "$resp" | send_expect_ok "t07 reply" >/dev/null
	sent_id=$(jq -r '.messages.sent_events[0].event_id // ""' <<<"$resp")
	if [ -z "$sent_id" ]; then
		log "t07: send summary has no event id; resolving the reply event from the room"
		sent_meta=$(newest_own_event "$room" "$since_ms" ".sender == \$me and (.relates_to[\"m.in_reply_to\"].event_id == \"$target_event\" or (.relates_to.rel_type == \"m.in_reply_to\" and .relates_to.event_id == \"$target_event\"))")
		[ -n "$sent_meta" ] || die "could not resolve the reply event in $room"
		sent_id=$(jq -r '.event_id' <<<"$sent_meta")
	fi
	[ -n "$sent_id" ] || die "no sent event id in the send JSON"
	log "reply event: $sent_id (token=${reply_token})"
fi

sent_meta=${sent_meta:-$(event_meta "$room" "$sent_id")}
log "reply event meta: $sent_meta"
jq -e --arg t "$target_event" '.relates_to["m.in_reply_to"].event_id == $t or (.relates_to.rel_type == "m.in_reply_to" and .relates_to.event_id == $t)' <<<"$sent_meta" >/dev/null ||
	die "sent event does not reply to $target_event: $sent_meta"
log "t07 matrix: reply relation confirmed (target=$target_event)"

log "t07 phone: polling handset $PHONE_SERIAL for the reply token (up to ${PHONE_SMS_TIMEOUT}s)"
deadline=$(( $(date +%s) + PHONE_SMS_TIMEOUT ))
found=0
sms_id=""
sms_count=0
while [ "$(date +%s)" -lt "$deadline" ] && [ "$found" -eq 0 ]; do
	matches=$(phone_db_query "$PHONE_SERIAL" "select _id from sms where date >= ${since_ms} and type = 1 and body like '%${reply_token}%' order by date desc" | tr -d '\r' || true)
	sms_count=$(grep -c . <<<"$matches" || true)
	if [ "$sms_count" -ge 1 ]; then
		found=1
		sms_id=$(head -n1 <<<"$matches")
		break
	fi
	sleep 5
done
[ "$found" -eq 1 ] || die "reply SMS (token=$reply_token) did not reach handset $PHONE_SERIAL within ${PHONE_SMS_TIMEOUT}s (event=$sent_id)"
if [ "$sms_count" -ne 1 ]; then
	log "t07 phone: WARNING token matched $sms_count handset rows (expected 1; concurrent traffic?)"
fi

prefix_info=$(phone_sms_body_prefix "$PHONE_SERIAL" "$sms_id" ">")
log "t07 phone: sms=$sms_id rows=$sms_count leading_quote=$(printf '%s' "$prefix_info" | cut -d' ' -f1) ($prefix_info)"

log "T07 replies OK (event=$sent_id target=$target_event sms=$sms_id)"
pass "T07"
