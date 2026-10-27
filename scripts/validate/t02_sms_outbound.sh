#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T02 — outbound SMS: send one message from the bridged portal room of a
# handset number to that handset and prove delivery by reading the handset's
# SMS inbox over adb (read-only).
#
# The portal room for PHONE_NUMBER must already exist (create it once with
# the bridge `create <number>@cheogram.com` command in the management room).
# The message carries a unique token (validate-t02-<unix-ts>); the message
# body is never printed — only its length, a sha256 prefix and the token
# match result.
#
# `send_wait` waits for a reply from a different sender. The receiving
# handset does not auto-reply, so a wait timeout is expected and recorded;
# the assertions are on the send result plus handset delivery.
#
# Usage:
#   PHONE_NUMBER=+15551234567 \
#   PHONE_SERIAL=<adb-serial> \
#   scripts/validate/t02_sms_outbound.sh
#
# Environment: see scripts/validate/lib.sh (PHONE_* vars select the handset).
# Optional tuning:
#   SEND_WAIT_TIMEOUT  seconds to wait for a reply (default: 25)
#   PHONE_SMS_TIMEOUT  seconds to poll the handset inbox (default: 120)
#   PHONE_SMS_POLL     seconds between inbox polls (default: 6)

set -euo pipefail
LIB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$LIB_DIR/lib.sh"

: "${SEND_WAIT_TIMEOUT:=25}"
: "${PHONE_SMS_TIMEOUT:=120}"
: "${PHONE_SMS_POLL:=6}"

require_env PHONE_NUMBER PHONE_SERIAL

token="validate-t02-$(date +%s)"
body="jmpchat bridge validation ${token}"
expected_hash=$(printf '%s' "$body" | sha256_16)

log "body (redacted): length=${#body} sha256_16=$expected_hash"

assert_room_count "$PHONE_NUMBER" 1
room=$(room_for_number "$PHONE_NUMBER")
[ -n "$room" ] || die "no portal room for $PHONE_NUMBER (create it with the bridge 'create' command first)"
log "room: $room"

start_ms=$(date +%s%3N)
resp=$(send_wait "$room" "$body" "$SEND_WAIT_TIMEOUT" || true)
[ -n "$resp" ] || die "send returned no JSON (is the iamb daemon on $IAMB_SSH running?)"
if ! jq -e '.messages.sent == 1 and .messages.failures == 0 and (.errors | length) == 0' <<<"$resp" >/dev/null; then
	jq -c '{sent: .messages.sent, failures: .messages.failures, errors: .errors}' <<<"$resp" >&2
	die "send reported failures"
fi
sent_id=$(jq -r '.messages.sent_events[0].event_id // ""' <<<"$resp")
log "sent event: $sent_id"

replies=$(jq '.wait.replies | length' <<<"$resp")
timeouts=$(jq '.wait.timeouts // 0' <<<"$resp")
if [ "$replies" -ge 1 ]; then
	reply_id=$(jq -r '.wait.replies[0].event_id' <<<"$resp")
	reply_hash=$(jq -r '.wait.replies[0].body' <<<"$resp" | sha256_16)
	log "unexpected reply: event=$reply_id body_sha256_16=$reply_hash"
else
	log "no reply within ${SEND_WAIT_TIMEOUT}s (timeouts=$timeouts) — expected for a receive-only handset"
fi

# Look at inbox rows from shortly before the send, whatever the sender is:
# the bridged SMS may arrive from a gateway number rather than the JMP number.
since_ms=$((start_ms - 600000))
log "polling handset $PHONE_SERIAL for the token (up to ${PHONE_SMS_TIMEOUT}s)"
deadline=$(( $(date +%s) + PHONE_SMS_TIMEOUT ))
rows=""
found=0
while [ "$(date +%s)" -lt "$deadline" ]; do
	rows=$(phone_sms_rows_since "$PHONE_SERIAL" "$since_ms" || true)
	if grep -qF -- "$token" <<<"$rows"; then
		found=1
		break
	fi
	sleep "$PHONE_SMS_POLL"
done
[ "$found" -eq 1 ] || die "token not found in handset inbox (sent event: $sent_id; no body logged) — is $PHONE_NUMBER a plain PSTN number? JMP/Cheogram numbers arrive in the Cheogram app, not content://sms"

row=$(grep -F -- "$token" <<<"$rows" | head -n1)
addr=$(sed -n 's/^Row: [0-9]* address=\([^,]*\),.*$/\1/p' <<<"$row")
date_ms=$(sed -n 's/^Row: [0-9]* address=[^,]*, date=\([0-9]*\),.*$/\1/p' <<<"$row")
# Everything after the first `body=` is the body; use parameter expansion
# (not sed) so no trailing newline sneaks into the hash.
row_body=${row#*body=}
body_hash=$(printf '%s' "$row_body" | sha256_16)
[ "$body_hash" = "$expected_hash" ] || die "handset body sha256_16=$body_hash does not match sent sha256_16=$expected_hash"

log "handset delivery confirmed: sender=$addr date=${date_ms}ms body_sha256_16=$body_hash"
log "T02 outbound SMS OK (sent=$sent_id)"
