#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T11 — new DM via the bridge `create` command, and its idempotency.
#
# Asks the bridge management room to `create <number>@cheogram.com`:
#   * when the portal already exists, the bridge must answer with a
#     "chat already exists" notice that references the existing room, and
#     no second room may appear (the harness asserts both);
#   * when it does not exist yet, a portal is created and must appear exactly
#     once (the harness accepts one new pending invite, like t03).
#
# One SMS is never sent by this test: `create` only creates the portal.
#
# Usage:
#   JMP_NUMBER=+15551234567 \
#   JMP_MGMT_ROOM='!managementRoomId' \
#   TARGET_PHONE=+15557654321 \
#   scripts/validate/t11_new_dm.sh
#
# Optional:
#   TARGET_JID   full JID to use instead of <TARGET_PHONE>@cheogram.com
#   PHONE_POLL_INTERVAL  seconds between portal polls (default 5)
#   IAMB_*       see lib.sh

set -euo pipefail
LIB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$LIB_DIR/lib.sh"

require_jmp_env
require_env JMP_MGMT_ROOM TARGET_PHONE

: "${PHONE_POLL_INTERVAL:=5}"

jid="${TARGET_JID:-${TARGET_PHONE}@cheogram.com}"
log "target JID: $jid"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

before_count=$(room_count_for_number "$TARGET_PHONE")
log "rooms matching $TARGET_PHONE before: $before_count"
if [ "$before_count" -gt 1 ]; then
	die "expected at most 1 existing room for $TARGET_PHONE, found $before_count"
fi

iamb_cmd 'invites' 2>/dev/null | grep -oE '![A-Za-z0-9_-]+' | sort > "$tmp/invites.before" || true

log "sending 'create $jid' to the management room"
resp=$(management_send_wait "create $jid" "$VALIDATE_TIMEOUT" || true)
[ -n "$resp" ] || die "management room send returned no JSON (JMP_MGMT_ROOM=$JMP_MGMT_ROOM)"
printf '%s' "$resp" | send_expect_ok "t11 create" >/dev/null
reply_id=$(jq -r '.wait.replies[0].event_id // ""' <<<"$resp")
reply_hash=$(jq -r '.wait.replies[0].body // ""' <<<"$resp" | sha256_16)
log "management reply: event=$reply_id body_sha256_16=$reply_hash"

room=$(room_for_number "$TARGET_PHONE")
if [ "$before_count" -eq 1 ]; then
	[ -n "$room" ] || die "room vanished between the count and the lookup"
	if jq -r '.wait.replies[0].body // ""' <<<"$resp" | grep -qF -- "$room"; then
		log "idempotency: reply references the existing room $room"
	else
		die "existing-chat reply does not reference $room (body_sha256_16=$reply_hash) — it may have created a second room"
	fi
	after_count=$(room_count_for_number "$TARGET_PHONE")
	[ "$after_count" -eq 1 ] || die "expected exactly 1 room after create, found $after_count (duplicate portal?)"
	log "T11 idempotent create OK (room=$room, rooms=$after_count)"
	pass "T11"
	exit 0
fi

# No room existed yet: the bridge must create exactly one.
log "no portal existed; waiting for the bridge to create it (up to ${VALIDATE_TIMEOUT}s)"
deadline=$(( $(date +%s) + VALIDATE_TIMEOUT ))
accepted=0
after_count=0
while :; do
	after_count=$(room_count_for_number "$TARGET_PHONE")
	[ "$after_count" -ge 1 ] && break
	if [ "$accepted" -eq 0 ]; then
		iamb_cmd 'invites' 2>/dev/null | grep -oE '![A-Za-z0-9_-]+' | sort > "$tmp/invites.now" || true
		comm -13 "$tmp/invites.before" "$tmp/invites.now" > "$tmp/invites.new" || true
		new_invites=$(wc -l < "$tmp/invites.new" | tr -d ' ')
		if [ "$new_invites" -eq 1 ]; then
			inv_room=$(cat "$tmp/invites.new")
			log "new pending invite $inv_room; accepting bridge-created portal"
			iamb_cmd "invites --accept '$inv_room'"
			accepted=1
		fi
	fi
	if [ "$(date +%s)" -ge "$deadline" ]; then
		die "portal did not appear within ${VALIDATE_TIMEOUT}s (rooms matching: $after_count)"
	fi
	sleep "$PHONE_POLL_INTERVAL"
done
[ "$after_count" -eq 1 ] || die "expected exactly 1 new room, found $after_count"
room=$(room_for_number "$TARGET_PHONE")
log "T11 new DM created (room=$room)"
pass "T11"
