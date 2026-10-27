#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T03 — group portal creation: ask the bridge to create one group text
# portal for two numbers via the `create` command in the management room,
# then assert that exactly one room whose display name matches the ascending
# comma-joined JID appears. Counting goes through room_count_for_number,
# which consults both `rooms .` and a targeted room search (fresh portals
# are sometimes missing from the full-room snapshot).
#
# No SMS is sent by this script; it only creates the portal. The new room
# id is the only identifier printed on stdout (display names contain phone
# numbers).
#
# Usage:
#   JMP_NUMBER=+15551234567 \
#   JMP_MGMT_ROOM='!...' \
#   GROUP_PHONE_A=+15105550001 GROUP_PHONE_B=+15105550002 \
#   scripts/validate/t03_group_create.sh
#
# Environment: see scripts/validate/lib.sh.
# Notes:
#   * numbers must be same-length E.164 so lexicographic sort equals numeric
#     order (JMP expects the ascending comma-separated JID form);
#   * if the bridge invites instead of joining the user, exactly one new
#     pending invite (not present before the run) is accepted automatically;
#   * re-running against an existing group room is a no-op that prints the
#     existing room id;
#   * bridge room creation is asynchronous, so the check polls.

set -euo pipefail
LIB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$LIB_DIR/lib.sh"

require_jmp_env
require_env JMP_MGMT_ROOM GROUP_PHONE_A GROUP_PHONE_B

: "${PHONE_POLL_INTERVAL:=5}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

numbers=$(printf '%s\n%s\n' "$GROUP_PHONE_A" "$GROUP_PHONE_B" | sort | paste -sd, -)
jid="${numbers}@cheogram.com"
log "group JID (ascending): $jid"

# The bridge names group rooms with the comma-joined numbers and may or may
# not append the domain, so lookups use the bare number list.
before_count=$(room_count_for_number "$numbers")
log "rooms matching group numbers before: $before_count"
if [ "$before_count" -ge 1 ]; then
	existing=$(room_for_number "$numbers")
	log "group room already exists, nothing to create"
	[ "$before_count" -eq 1 ] || die "expected at most 1 existing room, found $before_count"
	printf '%s\n' "$existing"
	exit 0
fi

iamb_cmd 'invites' 2>/dev/null | grep -oE '![A-Za-z0-9_-]+' | sort > "$tmp/invites.before" || true

log "sending 'create' to the management room"
resp=$(management_send_wait "create $jid" "$VALIDATE_TIMEOUT" || true)
[ -n "$resp" ] || die "management room send returned no JSON (JMP_MGMT_ROOM=$JMP_MGMT_ROOM)"
# Only the ids/hash summary is logged; the JSON carries the bot's reply body.
printf '%s' "$resp" | wait_expect_success "t03 create" >/dev/null

deadline=$(( $(date +%s) + VALIDATE_TIMEOUT ))
accepted=0
after_count=0
while :; do
	after_count=$(room_count_for_number "$numbers")
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
		log "management reply body_sha256_16=$(jq -r '.wait.replies[0].body' <<<"$resp" | sha256_16)"
		die "group room did not appear within ${VALIDATE_TIMEOUT}s (matching rooms: $after_count, expected 1)"
	fi
	sleep "$PHONE_POLL_INTERVAL"
done

[ "$after_count" -eq 1 ] || die "expected exactly 1 matching room, found $after_count"
new_id=$(room_for_number "$numbers")
new_name=$(rooms_matching "$numbers" | jq -r --arg id "$new_id" '.[] | select(.room_id == $id) | .name')
case "$new_name" in
"$numbers"*) ;;
*) die "new room $new_id does not match the group number prefix (name_sha256_16=$(printf '%s' "$new_name" | sha256_16))" ;;
esac

log "group room confirmed (matching rooms: $after_count)"
printf '%s\n' "$new_id"
log "T03 group create OK"
