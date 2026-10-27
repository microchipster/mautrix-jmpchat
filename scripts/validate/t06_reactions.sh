#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T06 — reactions, Matrix -> phone (and the inbound leg's capability).
#
# Matrix side: pick the newest message the phone sent (or REACT_TARGET_EVENT),
# send one Matrix reaction from iamb, and verify the reaction event exists and
# targets that message.
#
# Phone side: the connector sends XEP-0444 reactions to XMPP contacts, with a
# plain-body fallback appended; the Cheogram SMS gateway may or may not relay
# that fallback to a PSTN handset. The test therefore reports what actually
# arrived on the handset (`phone_fallback=arrived|none`) instead of failing on
# gateway behaviour, and checks it as evidence rather than a hard assertion.
#
# Inbound leg: PSTN SMS cannot carry XEP-0444, and the connector parses
# reactions only from XMPP stanzas (no text-based "Liked ..." parsing), so an
# SMS-side test would only prove plain-text delivery; the leg SKIPs with that
# reason. Set REACT_INBOUND=1 to additionally document a phone-side attempt.
#
# Usage:
#   JMP_NUMBER=+15551234567 PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
#   scripts/validate/t06_reactions.sh
#
# Optional:
#   REACT_TARGET_EVENT  react to this event id instead of the newest phone message
#   REACT_EMOJI         reaction key (default '👍')
#   REACT_TIMEOUT       seconds to poll the handset for a fallback (default 60)
#   PHONE_* / IAMB_*    see lib.sh

set -euo pipefail
LIB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$LIB_DIR/lib.sh"

: "${REACT_EMOJI:=👍}"
: "${REACT_TIMEOUT:=60}"

require_jmp_env
require_env PHONE_NUMBER PHONE_SERIAL

assert_room_count "$PHONE_NUMBER" 1
room=$(room_for_number "$PHONE_NUMBER")
[ -n "$room" ] || die "no portal room for $PHONE_NUMBER"
log "portal room: $room"

target_meta=""
if [ -n "${REACT_TARGET_EVENT:-}" ]; then
	target_event="$REACT_TARGET_EVENT"
	target_meta=$(event_meta "$room" "$target_event")
else
	target_meta=$(newest_phone_message "$room" 60)
	target_event=$(jq -r '.event_id // empty' <<<"$target_meta")
fi
if [ -z "$target_event" ]; then
	skip "no phone-side message in the recent archive to react to (run the attachment or reply tests first, or set REACT_TARGET_EVENT)"
fi
log "reaction target: $target_meta"

if [ -n "${REACT_EVENT:-}" ]; then
	react_id="$REACT_EVENT"
	react_meta=$(event_meta "$room" "$react_id")
	react_ts=$(jq -r '.ts // 0' <<<"$react_meta")
	since_ms=$(( react_ts - 2000 ))
	log "t06 matrix: verifying existing reaction event $react_id (no new reaction sent)"
else
	since_ms=$(date +%s%3N)
	resp=$(react_wait "$room" "$target_event" "$REACT_EMOJI" 15 || true)
	[ -n "$resp" ] || die "reaction send returned no JSON"
	printf '%s' "$resp" | send_expect_ok "t06 react" >/dev/null
	react_meta=$(newest_own_event "$room" "$since_ms" '.sender == $me and .type == "m.reaction"')
	[ -n "$react_meta" ] || die "no m.reaction event from the iamb user found after the send (reactions are not listed in the send summary)"
	react_id=$(jq -r '.event_id' <<<"$react_meta")
	log "reaction event: $react_id"
fi
react_meta=${react_meta:-$(event_meta "$room" "$react_id")}
log "reaction event meta: $react_meta"
jq -e '.type == "m.reaction"' <<<"$react_meta" >/dev/null ||
	die "expected an m.reaction event, got: $react_meta"
jq -e --arg t "$target_event" '.relates_to.rel_type == "m.annotation" and .relates_to.event_id == $t' <<<"$react_meta" >/dev/null ||
	die "reaction does not annotate the target: $react_meta"
log "t06 matrix: reaction confirmed (target=$target_event)"

log "t06 phone: polling handset $PHONE_SERIAL for a fallback message (up to ${REACT_TIMEOUT}s)"
deadline=$(( $(date +%s) + REACT_TIMEOUT ))
phone_fallback=none
phone_row=""
while [ "$(date +%s)" -lt "$deadline" ] && [ "$phone_fallback" = "none" ]; do
	rows=$(phone_sms_rows_db "$PHONE_SERIAL" "$since_ms" || true)
	row=$(awk -F'|' '$2 == 1 {print; exit}' <<<"$rows" || true)
	if [ -n "$row" ]; then
		phone_fallback=arrived
		phone_row="$row"
		break
	fi
	sleep 5
done

if [ "$phone_fallback" = "arrived" ]; then
	sms_id=$(printf '%s' "$phone_row" | cut -d'|' -f1)
	body=$(phone_db_query "$PHONE_SERIAL" "select body from sms where _id=${sms_id}" | tr -d '\r')
	body_sha=$(printf '%s' "$body" | sha256_16)
	emoji_seen=no
	case "$body" in
	*"$REACT_EMOJI"*) emoji_seen=yes ;;
	esac
	# The Cheogram gateway renders XEP-0444 reactions for SMS handsets as
	# a "Liked “<quoted text>”" text message.
	liked_seen=no
	case "$body" in
	"Liked "*) liked_seen=yes ;;
	esac
	log "t06 phone: sms=$sms_id len=${#body} sha256_16=$body_sha emoji_present=$emoji_seen liked_prefix=$liked_seen"
else
	log "t06 phone: no fallback message on the handset within ${REACT_TIMEOUT}s (gateway behaviour, not a bridge failure)"
fi

if [ "${REACT_INBOUND:-0}" = "1" ]; then
	log "t06 inbound: sending one emoji text from the handset to document SMS reaction handling"
	out=$(phone_control_send "$PHONE_SERIAL" "$JMP_NUMBER" "$REACT_EMOJI" || true)
	if [ -n "$out" ]; then
		jq -c '{message_id, mode, pending}' <<<"$out" >&2
		sleep 20
		archive_peek "$room" 5
	else
		log "t06 inbound: control.SEND returned nothing"
	fi
fi

log "T06 reactions OK (matrix reaction=$react_id target=$target_event phone_fallback=$phone_fallback)"
pass "T06"
