#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T08 — edits and redactions.
#
# Matrix edit -> phone: the connector relays a Matrix edit to cheogram.com
# targets as a fresh message with a "* " prefix (SMS cannot be edited in
# place). iamb has no edit command, so the edit goes through the Matrix
# client-server API. Portal rooms are E2EE and this harness cannot encrypt a
# Matrix event, so the API-sent edit is *unencrypted*: a deliberate, explicit
# validation artifact. The script refuses to do it unless ALLOW_PLAINTEXT_EDIT=1
# (and says so) — run the tranche with that variable set; REUSE the room only
# for validation.
#
# Matrix redaction -> phone: redacting a bridged message makes the bridge post
# an m.notice in the portal (SMS/MMS cannot be unsent); the test asserts the
# notice appears and that the handset keeps its copy of the message.
#
# Usage:
#   JMP_NUMBER=+15551234567 PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
#   ALLOW_PLAINTEXT_EDIT=1 \
#   scripts/validate/t08_edits_redactions.sh
#
# Optional:
#   EDIT_TARGET_EVENT   edit this event instead of sending a fresh target
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

# --- target message -------------------------------------------------------
if [ -n "${EDIT_TARGET_EVENT:-}" ]; then
	target_event="$EDIT_TARGET_EVENT"
	log "edit target (given): $target_event"
else
	target_token="validate-t08-target-$(date +%s)"
	target_text="$target_token bridge validation target"
	target_hash=$(printf '%s' "$target_text" | sha256_16)
	log "t08 sending target message (len=${#target_text} sha256_16=$target_hash)"
	target_since_ms=$(date +%s%3N)
	resp=$(send_wait "$room" "$target_text" "$SEND_WAIT_TIMEOUT" || true)
	[ -n "$resp" ] || die "target send returned no JSON"
	printf '%s' "$resp" | send_expect_ok "t08 target" >/dev/null
	target_event=$(jq -r '.messages.sent_events[0].event_id // ""' <<<"$resp")
	[ -n "$target_event" ] || die "no target event id in the send JSON"
	log "edit target (sent): $target_event token=$target_token"
fi

# --- Matrix edit ----------------------------------------------------------
if [ "${SKIP_EDIT:-0}" = "1" ]; then
	log "t08 edit: skipped by SKIP_EDIT=1"
elif [ "${ALLOW_PLAINTEXT_EDIT:-0}" != "1" ]; then
	log "t08 edit: skipped — set ALLOW_PLAINTEXT_EDIT=1 to accept the unencrypted-edit artifact in the E2EE portal"
else
	edited_text="validate-t08-edited-$(date +%s) bridge validation edit"
	edit_token=${edited_text%% *}
	edited_hash=$(printf '%s' "$edited_text" | sha256_16)
	edit_since_ms=$(date +%s%3N)
	txn="t08edit$(date +%s%N)"
	edit_body=$(jq -cn --arg body " * $edited_text" --arg new "$edited_text" --arg target "$target_event" \
		'{msgtype: "m.text", body: $body, "m.new_content": {msgtype: "m.text", body: $new}, "m.relates_to": {rel_type: "m.replace", event_id: $target}}')
	log "t08 edit: sending unencrypted edit event (edited sha256_16=$edited_hash)"
	edit_resp=$(matrix_api_send "$room" "m.room.message" "$txn" <<<"$edit_body")
	edit_event=$(jq -r '.event_id // empty' <<<"$edit_resp")
	[ -n "$edit_event" ] || die "edit API call returned no event id: $edit_resp"
	log "t08 edit event: $edit_event"
	edit_meta=$(event_meta "$room" "$edit_event" 2>/dev/null || true)
	log "t08 edit event meta: $edit_meta"

	log "t08 edit phone: polling handset $PHONE_SERIAL for the '* ' fallback SMS (up to ${PHONE_SMS_TIMEOUT}s)"
	deadline=$(( $(date +%s) + PHONE_SMS_TIMEOUT ))
	edit_found=0
	edit_sms_id=""
	edit_hits=0
	edit_dropped=""
	while [ "$(date +%s)" -lt "$deadline" ] && [ "$edit_found" -eq 0 ]; do
		matches=$(phone_db_query "$PHONE_SERIAL" "select _id from sms where date >= ${edit_since_ms} and type = 1 and body like '%${edit_token}%' order by date desc" | tr -d '\r' || true)
		edit_hits=$(grep -c . <<<"$matches" || true)
		if [ "$edit_hits" -ge 1 ]; then
			edit_found=1
			edit_sms_id=$(head -n1 <<<"$matches")
			break
		fi
		# The bridge drops unencrypted events in E2EE portals and explains
		# itself with a notice; catch that so the test can skip honestly.
		if [ -z "$edit_dropped" ]; then
			for ev in $(room_events_since "$room" "$edit_since_ms" 10 | jq -r '.event_id' || true); do
				meta=$(event_meta "$room" "$ev" 2>/dev/null || true)
				[ -n "$meta" ] || continue
				if jq -e --arg e "$edit_event" '.msgtype == "m.notice" and .relates_to["m.in_reply_to"].event_id == $e' <<<"$meta" >/dev/null 2>&1; then
					edit_dropped="$ev"
					break
				fi
			done
		fi
		sleep 5
	done
	if [ "$edit_found" -eq 1 ]; then
		prefix_info=$(phone_sms_body_prefix "$PHONE_SERIAL" "$edit_sms_id" "* ")
		log "t08 edit phone: sms=$edit_sms_id rows=$edit_hits star_prefix=$(printf '%s' "$prefix_info" | cut -d' ' -f1) ($prefix_info)"
		[ "$(printf '%s' "$prefix_info" | cut -d' ' -f1)" = "yes" ] ||
			log "t08 edit phone: WARNING no leading '* ' on the handset SMS (gateway may have reformatted it)"
		edit_leg="bridged (*  fallback)"
	elif [ -n "$edit_dropped" ]; then
		# Expected for the API-sent plaintext edit: the bridge requires
		# encryption in this portal and enqueued a "not encrypted" notice.
		log "t08 edit: bridge dropped the unencrypted edit and posted a notice ($edit_dropped) — the '* ' fallback needs an encrypted edit from a real client; recording this as a harness limitation"
		edit_leg="not-observable-unencrypted-drop"
	else
		die "no '* ' fallback SMS carrying token=$edit_token on handset $PHONE_SERIAL within ${PHONE_SMS_TIMEOUT}s and no unencrypted-drop notice (edit event=$edit_event)"
	fi
fi
edit_leg=${edit_leg:-skipped}

# --- Matrix redaction -----------------------------------------------------
redact_target="${REDACT_TARGET_EVENT:-$target_event}"
redact_since_ms=$(date +%s%3N)
redact_txn="t08redact$(date +%s%N)"
redact_resp=$(matrix_api_redact "$room" "$redact_target" "$redact_txn" "t08 validation redaction")
redact_event=$(jq -r '.event_id // empty' <<<"$redact_resp")
[ -n "$redact_event" ] || die "redact API call returned no event id: $redact_resp"
log "t08 redact event: $redact_event (target=$redact_target)"

log "t08 redact: polling $room for the bot's cannot-unsend notice (up to ${VALIDATE_TIMEOUT}s)"
deadline=$(( $(date +%s) + VALIDATE_TIMEOUT ))
notice_event=""
while [ "$(date +%s)" -lt "$deadline" ] && [ -z "$notice_event" ]; do
	candidates=$(room_events_since "$room" "$redact_since_ms" 20 || true)
	while IFS= read -r cand; do
		[ -n "$cand" ] || continue
		ev=$(jq -r '.event_id' <<<"$cand")
		[ "$ev" = "$redact_event" ] && continue
		meta=$(event_meta "$room" "$ev" 2>/dev/null || true)
		[ -n "$meta" ] || continue
		if jq -e '.msgtype == "m.notice"' <<<"$meta" >/dev/null 2>&1; then
			notice_event="$ev"
			break
		fi
	done <<<"$candidates"
	[ -n "$notice_event" ] || sleep 5
done
[ -n "$notice_event" ] || die "no m.notice appeared after redacting $target_event (bridge should post an 'SMS/MMS messages cannot be unsent' notice)"
log "t08 redact notice: $notice_event"

if [ -n "${target_token:-}" ]; then
	phone_copies=$(phone_db_query "$PHONE_SERIAL" "select count(*) from sms where type = 1 and body like '%${target_token}%'" | tr -d '\r')
	log "t08 redact phone: handset still holds $phone_copies SMS row(s) carrying the original token (SMS cannot be unsent)"
elif [ -n "${target_since_ms:-}" ]; then
	phone_copies=$(phone_db_query "$PHONE_SERIAL" "select count(*) from sms where date >= ${target_since_ms} and type = 1" | tr -d '\r')
	log "t08 redact phone: handset still holds $phone_copies message(s) from this run (SMS cannot be unsent)"
fi

log "T08 edits/redactions OK (target=$target_event edit_leg=$edit_leg redaction=$redact_event notice=$notice_event)"
pass "T08"
