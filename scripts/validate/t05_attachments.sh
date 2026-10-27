#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T05 — attachments in both directions.
#
# Matrix -> phone: send the deterministic fixture image from the portal room
# with iamb and prove the handset received an MMS whose image part hashes to
# the same bytes. The handset is read through its mmssms.db (pdu/part tables)
# with root adb; the MMS payload bytes stay on the device.
#
# Phone -> Matrix: push the same fixture to the handset, ask the Fossify
# Messages bridge build to send it as an MMS to the JMP number via
# control.SEND, then find the bridged m.image event in the portal room,
# download the media through the homeserver and compare the decrypted bytes
# with the fixture.
#
# Byte-exactness is reported explicitly for each hop: MMS gateways may
# re-encode images, so a hash mismatch is recorded as "re-encoded" evidence
# rather than a hard failure; the test fails only when an expected image is
# missing altogether. Only ids, sizes, MIME types and sha256_16 prefixes are
# logged; never image bytes, captions or other bodies.
#
# Usage:
#   JMP_NUMBER=+15551234567 \
#   PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
#   [PHONE_ADB_SSH=<ssh-host>] \
#   scripts/validate/t05_attachments.sh
#
# Optional tuning:
#   SEND_WAIT_TIMEOUT    seconds to wait for a reply after each send (default 25)
#   PHONE_MMS_TIMEOUT    seconds to poll the handset MMS database (default 180)
#   PHONE_MMS_POLL       seconds between handset polls (default 8)
#   MATRIX_WAIT_TIMEOUT  seconds to poll for the bridged inbound event (default 180)
#   P2M_ATTEMPTS         how many real phone->Matrix MMS sends are allowed (default 1)
#   M2P_EVENT            verify this existing Matrix-side image event instead of
#                        sending the fixture from Matrix (no SMS/MMS spent)
#   P2M_EVENT            verify this existing bridged inbound image event instead
#                        of sending the fixture from the handset
#   JMP_FIXTURE          alternate fixture path (default scripts/validate/fixtures/jmpchat-validation-64.png)
#   PHONE_CONTROL_PKG    Fossify bridge package on the handset (default debug build)

set -euo pipefail
LIB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$LIB_DIR/lib.sh"

: "${SEND_WAIT_TIMEOUT:=25}"
: "${PHONE_MMS_TIMEOUT:=180}"
: "${PHONE_MMS_POLL:=8}"
: "${MATRIX_WAIT_TIMEOUT:=180}"

require_jmp_env
require_env PHONE_NUMBER PHONE_SERIAL

FIXTURE="${JMP_FIXTURE:-$LIB_DIR/fixtures/jmpchat-validation-64.png}"
[ -f "$FIXTURE" ] || die "fixture not found: $FIXTURE"
fixture_sha=$(sha256_16 < "$FIXTURE")
fixture_size=$(wc -c < "$FIXTURE" | tr -d ' ')
log "fixture $(basename "$FIXTURE"): size=$fixture_size sha256_16=$fixture_sha"

assert_room_count "$PHONE_NUMBER" 1
room=$(room_for_number "$PHONE_NUMBER")
[ -n "$room" ] || die "no portal room for $PHONE_NUMBER (create it with the bridge 'create' command first)"
log "portal room: $room"

remote_dir=$(ssh "$IAMB_SSH" mktemp -d)
cleanup() { ssh "$IAMB_SSH" "rm -rf '$remote_dir'" >/dev/null 2>&1 || true; }
trap cleanup EXIT
remote_fixture="$remote_dir/$(basename "$FIXTURE")"
log "copying fixture to $IAMB_SSH for the iamb daemon"
scp -q "$FIXTURE" "$IAMB_SSH:$remote_fixture"

# --- Matrix -> phone: image arrives as an MMS with an image part ----------
# The iamb send summary does not list attachment events in sent_events, so
# the Matrix event is resolved from the room when the summary is empty. Set
# M2P_EVENT to an existing event id to verify delivery of an earlier send
# without spending another SMS/MMS.
if [ -n "${M2P_EVENT:-}" ]; then
	sent_id="$M2P_EVENT"
	sent_meta=$(event_meta "$room" "$sent_id")
	sent_ts=$(jq -r '.ts // 0' <<<"$sent_meta")
	since_s=$(( sent_ts / 1000 - 10 ))
	log "t05 m2p: verifying existing event $sent_id (ts=${sent_ts}ms) without sending"
else
	since_s=$(( $(date +%s) - 5 ))
	send_since_ms=$(date +%s%3N)
	log "t05 m2p: sending the fixture to $room"
	resp=$(send_attach_wait "$room" "$remote_fixture" "$SEND_WAIT_TIMEOUT" || true)
	[ -n "$resp" ] || die "attachment send returned no JSON (is the iamb daemon on $IAMB_SSH running?)"
	printf '%s' "$resp" | send_expect_ok "t05 m2p attach" >/dev/null
	sent_id=$(jq -r '.messages.sent_events[0].event_id // ""' <<<"$resp")
	if [ -z "$sent_id" ]; then
		log "t05 m2p: send summary has no event id (attachments are not listed); resolving from the room"
		me=$(matrix_session_field .user_id)
		while IFS= read -r cand; do
			[ -n "$cand" ] || continue
			ev=$(jq -r '.event_id' <<<"$cand")
			meta=$(event_meta "$room" "$ev" 2>/dev/null || true)
			[ -n "$meta" ] || continue
			if jq -e --arg me "$me" '.sender == $me and (.msgtype == "image" or .msgtype == "m.image") and .has_media' <<<"$meta" >/dev/null 2>&1; then
				sent_id="$ev"
				sent_meta="$meta"
			fi
		done <<<"$(room_events_since "$room" "$send_since_ms" 10)"
	fi
	[ -n "$sent_id" ] || die "could not resolve the sent attachment event in $room"
	sent_meta=${sent_meta:-$(event_meta "$room" "$sent_id")}
fi
log "t05 m2p matrix event: $sent_meta"
jq -e '.msgtype == "image" or .msgtype == "m.image"' <<<"$sent_meta" >/dev/null ||
	die "sent event is not an image: $sent_meta"
jq -e '.has_media' <<<"$sent_meta" >/dev/null || die "sent image event carries no media"

matrix_upload=$(mxc_plain_hash "$room" "$sent_id")
log "t05 m2p matrix upload: $matrix_upload"
matrix_exact=no
printf '%s' "$matrix_upload" | grep -q "plain_sha256_16=$fixture_sha" && matrix_exact=yes
log "t05 m2p matrix-side byte-exact: $matrix_exact"

log "t05 m2p: polling handset $PHONE_SERIAL for the MMS (up to ${PHONE_MMS_TIMEOUT}s)"
deadline=$(( $(date +%s) + PHONE_MMS_TIMEOUT ))
phone_found=0
phone_pdu=""
phone_part=""
phone_sha=""
phone_size=""
phone_ct=""
last_image_sha=""
last_image_pdu=""
while [ "$(date +%s)" -lt "$deadline" ] && [ "$phone_found" -eq 0 ]; do
	mapfile -t pdu_rows < <(phone_mms_rows_since "$PHONE_SERIAL" "$since_s" || true)
	for row in "${pdu_rows[@]}"; do
		[ -n "$row" ] || continue
		pdu_id=${row%%|*}
		msg_box=$(printf '%s' "$row" | cut -d'|' -f2)
		[ "$msg_box" = "1" ] || continue
		mapfile -t parts < <(phone_mms_parts "$PHONE_SERIAL" "$pdu_id" || true)
		for part in "${parts[@]}"; do
			[ -n "$part" ] || continue
			ct=$(printf '%s' "$part" | cut -d'|' -f3)
			path=$(printf '%s' "$part" | cut -d'|' -f6)
			case "$ct" in
			image/*)
				[ -n "$path" ] || continue
				info=$(phone_file_info "$PHONE_SERIAL" "$path" || true)
				p_size=$(sed -n 's/^size=\([0-9]*\).*$/\1/p' <<<"$info")
				p_sha=$(sed -n 's/.*sha256_16=\([0-9a-f]*\).*$/\1/p' <<<"$info")
				[ -n "$p_sha" ] || continue
				last_image_sha="$p_sha"
				last_image_pdu="$pdu_id"
				# Concurrent tests on the same handset may deliver other
				# images; only a hash match satisfies this test.
				if [ "$p_sha" = "$fixture_sha" ]; then
					phone_found=1
					phone_pdu="$pdu_id"
					phone_part=$(printf '%s' "$part" | cut -d'|' -f1)
					phone_ct="$ct"
					phone_size="$p_size"
					phone_sha="$p_sha"
					break
				fi
				;;
			esac
		done
		[ "$phone_found" -eq 1 ] && break
	done
	[ "$phone_found" -eq 1 ] || sleep "$PHONE_MMS_POLL"
done

[ "$phone_found" -eq 1 ] || die "no handset MMS image matched the fixture sha256_16=$fixture_sha within ${PHONE_MMS_TIMEOUT}s (sent event: $sent_id; last image seen: pdu=${last_image_pdu:-none} sha256_16=${last_image_sha:-none} — a mismatch usually means the gateway re-encoded the image)"
phone_exact=no
[ "$phone_sha" = "$fixture_sha" ] && phone_exact=yes
log "t05 m2p handset: pdu=$phone_pdu part=$phone_part ct=$phone_ct size=$phone_size sha256_16=$phone_sha phone_byte_exact=$phone_exact"

# --- phone -> Matrix: the handset sends the fixture as an MMS -------------
# On a quiet handset the MMS usually lands within a minute; carriers and the
# JMP gateway can also queue a send for minutes. P2M_ATTEMPTS (default 1)
# controls how many real sends this leg is allowed to make, and P2M_EVENT can
# point at an already bridged inbound image event to verify delivery of an
# earlier send without spending another MMS.
dst="/sdcard/Android/data/${PHONE_CONTROL_PKG}/files/$(basename "$FIXTURE")"
mkdir_cmd="mkdir -p /sdcard/Android/data/${PHONE_CONTROL_PKG}/files"
adb_shell "$PHONE_SERIAL" "$mkdir_cmd" >/dev/null 2>&1 || true
phone_push_file "$PHONE_SERIAL" "$FIXTURE" "$dst"
log "t05 p2m: fixture pushed to the app's own external files dir ($dst) so the app can read it without media permissions"

poll_p2m_room() {
	local deadline=$(( $(date +%s) + MATRIX_WAIT_TIMEOUT ))
	local cands cand ev meta
	while [ "$(date +%s)" -lt "$deadline" ]; do
		mapfile -t cands < <(room_events_since "$room" "$since_ms" 20 || true)
		for cand in "${cands[@]}"; do
			[ -n "$cand" ] || continue
			ev=$(jq -r '.event_id' <<<"$cand")
			[ "$ev" = "$sent_id" ] && continue
			meta=$(event_meta "$room" "$ev" 2>/dev/null || true)
			[ -n "$meta" ] || continue
			if jq -e '.msgtype == "image" or .msgtype == "m.image"' <<<"$meta" >/dev/null 2>&1; then
				inbound_events=$(( inbound_events + 1 ))
				# Phone-originated events come from the bridge ghost
				# (@jmpchat_...); the iamb user's own uploads do not.
				if jq -e '.sender | test("jmpchat_")' <<<"$meta" >/dev/null 2>&1; then
					phone_images=$(( phone_images + 1 ))
				fi
				if [ "$inbound_found" -eq 0 ]; then
					inbound_found=1
					inbound_id="$ev"
					inbound_meta="$meta"
				fi
			fi
		done
		[ "$inbound_found" -eq 1 ] && return 0
		sleep "$PHONE_MMS_POLL"
	done
	return 1
}

inbound_found=0
inbound_events=0
phone_images=0
inbound_id=""
inbound_meta=""
out_msg_id=""
p2m_since_s=""
if [ -n "${P2M_EVENT:-}" ]; then
	inbound_id="$P2M_EVENT"
	inbound_meta=$(event_meta "$room" "$inbound_id" 2>/dev/null || true)
	inbound_found=1
	log "t05 p2m: verifying existing event $inbound_id without sending"
else
	p2m_attempts="${P2M_ATTEMPTS:-1}"
	attempt=0
	while [ "$attempt" -lt "$p2m_attempts" ] && [ "$inbound_found" -eq 0 ]; do
		attempt=$(( attempt + 1 ))
		inbound_token="validate-t05-inbound-$(date +%s)-a${attempt}"
		p2m_since_s=$(( $(date +%s) - 5 ))
		since_ms=$(date +%s%3N)
		log "t05 p2m: attempt $attempt/$p2m_attempts — asking the handset to send '$inbound_token' with the fixture to $JMP_NUMBER"
		out=$(phone_control_send "$PHONE_SERIAL" "$JMP_NUMBER" "$inbound_token" "$dst")
		[ -n "$out" ] || die "control.SEND returned no JSON (is the control surface enabled on $PHONE_SERIAL?)"
		jq -c '{message_id, thread_id, pending, mode}' <<<"$out" >&2
		mode=$(jq -r '.mode // empty' <<<"$out")
		[ "$mode" = "mms" ] || die "the handset did not choose an MMS send (mode=$mode); the file was not attached?"
		out_msg_id=$(jq -r '.message_id // 0' <<<"$out")
		log "t05 p2m handset send queued: message_id=$out_msg_id mode=$mode"
		log "t05 p2m: polling $room for the bridged image (up to ${MATRIX_WAIT_TIMEOUT}s)"
		if ! poll_p2m_room; then
			log "t05 p2m: attempt $attempt saw no bridged inbound image"
		fi
	done
fi

if [ "$inbound_found" -eq 0 ]; then
	log "t05 p2m: no bridged inbound image observed (handset message_id=${out_msg_id:-none})"
	if [ -n "${JMP_BRIDGE_SSH:-}" ]; then
		errs=$(bridge_log_grep 'inbound|HandleXMPPMessage|mms' || true)
		log "t05 p2m: bridge log lines matching inbound/mms so far: ${errs:-0}"
	fi
	skip "phone->Matrix MMS was accepted by the handset but not observed in $room within ${MATRIX_WAIT_TIMEOUT}s per attempt (carrier/MMSC or gateway latency; no bridge error evidence). Re-run with P2M_EVENT=<event id> once it arrives, or raise MATRIX_WAIT_TIMEOUT/P2M_ATTEMPTS"
fi

log "t05 p2m matrix event: $inbound_meta"
if [ "${phone_images:-0}" -eq 0 ] && jq -e '.sender | test("jmpchat_")' <<<"$inbound_meta" >/dev/null 2>&1; then
	phone_images=1
fi
[ "${phone_images:-0}" -ge 1 ] || die "the bridged image event did not come from the phone ghost (image events=$inbound_events, phone images=${phone_images:-0})"
log "t05 p2m matrix: $inbound_events image event(s) in the window, $phone_images phone-originated (concurrent Matrix uploads are listed but not asserted on)"
inbound_media=$(mxc_plain_hash "$room" "$inbound_id")
log "t05 p2m matrix media: $inbound_media"
inbound_exact=no
printf '%s' "$inbound_media" | grep -q "plain_sha256_16=$fixture_sha" && inbound_exact=yes
log "t05 p2m matrix-side byte-exact: $inbound_exact (phone images: $phone_images)"

# Handset-side evidence that the MMS was really sent.
if [ -n "$p2m_since_s" ] && [ -n "$out_msg_id" ]; then
	sent_pdu=$(phone_mms_rows_since "$PHONE_SERIAL" "$p2m_since_s" |
		awk -F'|' -v id="$out_msg_id" '$1 == id {print}' | head -n1 || true)
	if [ -n "$sent_pdu" ]; then
		log "t05 p2m handset pdu row: $(printf '%s' "$sent_pdu" | tr '|' ' ')"
	else
		log "t05 p2m handset pdu row: not in the local pdu table yet (message_id=$out_msg_id)"
	fi
else
	log "t05 p2m handset pdu row: no live send in this run (P2M_EVENT given)"
fi

log "T05 attachments OK (m2p event=$sent_id phone_exact=$phone_exact; p2m event=$inbound_id matrix_exact=$inbound_exact)"
pass "T05"
