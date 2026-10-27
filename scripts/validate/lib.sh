#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Shared helpers for the jmpchat bridge validation harness.
#
# Everything is driven over SSH against an `iamb` daemon (and, in later
# tests, adb against physical handsets). Nothing machine-specific is
# hardcoded: all values come from environment variables, so the harness
# works for anyone with an iamb profile and a JMP number.
#
# Required environment variables:
#   JMP_NUMBER        JMP.chat test number in E.164 form (e.g. +15551234567)
#   IAMB_SSH          SSH host that runs the iamb daemon
#
# Optional environment variables:
#   IAMB_BIN          iamb binary path on that host (default: ~/.cargo/bin/iamb)
#   IAMB_PROFILE      iamb profile name (default: public)
#   IAMB_ARCHIVE_DIR  Archive directory on IAMB_SSH
#                     (default: ~/.local/share/iamb/profiles/$IAMB_PROFILE/events)
#   IAMB_SESSION_JSON iamb session file on IAMB_SSH, source of the read-only
#                     client-server API token
#                     (default: ~/.local/share/iamb/profiles/$IAMB_PROFILE/session.json)
#   JMP_MGMT_ROOM     Management room ID of the bridge (!...), needed by
#                     management_send_wait
#   VALIDATE_TIMEOUT  Default wait timeout in seconds (default: 90)
#   KEEP_TMP          Set to keep temporary files for debugging
#   PHONE_SERIAL      adb serial of the handset to read, needed by the
#                     phone_sms_rows / adb_shell helpers
#   PHONE_ADB_SSH     SSH host that has the handset attached over adb;
#                     when unset, adb runs on the local machine
#   PHONE_USE_SU      Set to 1 to run handset commands via `su`
#                     (for devices that restrict reads to root)
#   PHONE_DB          mmssms.db path on the handset
#                     (default: the AOSP telephony provider path)
#   PHONE_CONTROL_PKG Fossify Messages bridge build package name
#                     (default: org.fossify.messages.bridge.debug; the release
#                     build is org.fossify.messages.bridge)
#   JMP_BRIDGE_SSH    SSH host that runs the bridge container; unset means
#                     restart tests SKIP
#   JMP_BRIDGE_SERVICE systemd service / JMP_BRIDGE_CONTAINER container name
#                     (default: matrix-mautrix-jmpchat)
#   BRIDGE_RESTART_TIMEOUT  seconds to wait for the bridge to come back
#                     (default: 90)
#
# Conventions (see todo.md):
#   * never log message bodies; log IDs, senders, timestamps and hashes only
#   * script only with full !room_id values — fuzzy room names can hang iamb
#   * sends always use --wait --reply-mode different-sender
#   * keep bursts small: JMP's terms allow sanctioned low-volume testing only

set -euo pipefail

: "${IAMB_SSH:=}"
: "${IAMB_BIN:=~/.cargo/bin/iamb}"
: "${IAMB_PROFILE:=public}"
: "${IAMB_ARCHIVE_DIR:=~/.local/share/iamb/profiles/$IAMB_PROFILE/events}"
: "${VALIDATE_TIMEOUT:=90}"

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() { log "FAIL: $*"; exit 1; }

require_env() {
	local missing=0
	for name in "$@"; do
		if [ -z "${!name:-}" ]; then
			log "Missing required environment variable: $name"
			missing=1
		fi
	done
	if [ "$missing" -ne 0 ]; then
		die "environment not set up (see scripts/validate/lib.sh)"
	fi
}

require_jmp_env() {
	require_env JMP_NUMBER
}

# iamb_cmd runs a single iamb subcommand over SSH. Pass the subcommand as one
# string; quote !room ids and message text in the caller.
iamb_cmd() {
	require_env IAMB_SSH
	ssh "$IAMB_SSH" "$IAMB_BIN -P '$IAMB_PROFILE' $*"
}

rooms_json() {
	iamb_cmd 'rooms . --format json'
}

# rooms_search QUERY runs a targeted local room search. Unlike `rooms .` it
# also finds freshly created portals (and the bridge management room) that
# the full-room snapshot misses.
rooms_search() {
	iamb_cmd "rooms '$1' --format json"
}

# rooms_match_filter matches display names against TARGET: exact, or the
# target followed by a space or `@`. A bare number therefore does not match
# a group room whose name merely starts with the same number, while
# `+15551234567` still matches `+15551234567 (jmp)` and
# `+15551234567@cheogram.com (jmp)`.
rooms_match_filter() {
	jq -c --arg n "$1" \
		'.matches[] | select(.name == $n or (.name | startswith($n + " ")) or (.name | startswith($n + "@"))) | {room_id, name}'
}

# rooms_matching TARGET merges `rooms .` with a targeted search, deduplicated
# by room id.
rooms_matching() {
	local target="$1"
	{
		rooms_json | rooms_match_filter "$target"
		rooms_search "$target" | rooms_match_filter "$target"
	} | jq -s -c 'unique_by(.room_id)'
}

# room_for_number prints the room id whose name matches NUMBER, or nothing
# when there is no such room.
room_for_number() {
	local number="$1"
	rooms_matching "$number" | jq -r '.[0].room_id // empty'
}

# room_count_for_number counts matching rooms; the group invariant expects 1.
room_count_for_number() {
	local number="$1"
	rooms_matching "$number" | jq -r 'length'
}

room_info() {
	local room="$1"
	iamb_cmd "room info '$room'"
}

# send_wait sends TEXT to ROOM, waits for a different-sender reply and prints
# the wait JSON on stdout. The text travels over stdin, so it never appears in
# the remote process list.
#
# --no-ipc is required: the patched iamb CLI reads --stdin in the invoking
# process only on the local execution path. Without it the command is
# forwarded to the running daemon verbatim and rejected with
# "--stdin must be read by the invoking CLI process before send execution".
# With --no-ipc the CLI reads stdin, fails on the daemon-held store lock and
# hands the send (body inline) over to the daemon.
send_wait() {
	require_env IAMB_SSH
	local room="$1"
	local text="$2"
	local timeout="${3:-$VALIDATE_TIMEOUT}"
	printf '%s' "$text" | ssh "$IAMB_SSH" \
		"$IAMB_BIN -P '$IAMB_PROFILE' --no-ipc send --room '$room' --stdin --wait --timeout '$timeout' --reply-mode different-sender --wait-output json"
}

# management_send_wait sends TEXT to the bridge management room.
management_send_wait() {
	require_env IAMB_SSH
	local text="$1"
	local timeout="${2:-$VALIDATE_TIMEOUT}"
	require_env JMP_MGMT_ROOM
	printf '%s' "$text" | ssh "$IAMB_SSH" \
		"$IAMB_BIN -P '$IAMB_PROFILE' --no-ipc send --room '$JMP_MGMT_ROOM' --stdin --wait --timeout '$timeout' --reply-mode different-sender --wait-output json"
}

# wait_expect_success validates the wait JSON on stdin: no send failures, no
# errors and at least one matched reply. Prints the matched reply body hash
# (bodies are never printed) plus the event ids.
wait_expect_success() {
	local label="$1"
	local json
	json=$(cat)
	if ! jq -e '.messages.failures == 0 and (.errors | length) == 0 and (.wait.timeouts == 0)' <<<"$json" >/dev/null; then
		jq -c '{failures: .messages.failures, errors: .errors, timeouts: .wait.timeouts, sent_events: .messages.sent_events}' <<<"$json" >&2
		die "$label: send or wait failures"
	fi
	local reply_count
	reply_count=$(jq '.wait.replies | length' <<<"$json")
	[ "$reply_count" -ge 1 ] || die "$label: no reply matched"
	local body_hash
	body_hash=$(jq -r '.wait.replies[0].body' <<<"$json" | sha256sum | cut -c1-16)
	local sent_id reply_id
	sent_id=$(jq -r '.messages.sent_events[0].event_id // ""' <<<"$json")
	reply_id=$(jq -r '.wait.replies[0].event_id' <<<"$json")
	log "$label: ok (sent=$sent_id reply=$reply_id body_sha256_16=$body_hash)"
	printf '%s' "$json"
}

# assert_room_count checks how many rooms match a number (group invariant: 1).
assert_room_count() {
	local number="$1"
	local want="$2"
	local got
	got=$(room_count_for_number "$number")
	[ "$got" -eq "$want" ] || die "expected $want room(s) for $number, found $got"
	log "room count for $number: $got (expected $want)"
}

# assert_bridge_room greps the room info output for the bridge marker.
assert_bridge_room() {
	local room="$1"
	local info
	info=$(room_info "$room")
	if grep -qiE 'Created by:.*@jmpchatbot' <<<"$info" || grep -qiE 'Bridge:.*jmpchat' <<<"$info"; then
		log "bridge marker present for $room"
		return 0
	fi
	die "no bridge marker in room info for $room (expected a jmpchatbot creator or a Bridge: line)"
}

# remote_path PATH rewrites a leading ~ into $HOME so the remote shell
# expands it even when the path sits inside quotes in the ssh command.
remote_path() { printf '%s' "${1/#\~/\$HOME}"; }

# archive_peek prints recent archive lines (IDs/attributes only, never bodies).
archive_peek() {
	require_env IAMB_SSH
	local room="$1"
	local lines="${2:-5}"
	local dir
	dir=$(remote_path "$IAMB_ARCHIVE_DIR")
	ssh "$IAMB_SSH" "tail -n '$lines' \"$dir/${room#!}\" 2>/dev/null" |
		jq -c '{event_id, sender, ts: .timestamp, body_len: ((.body // "") | length)}' 2>/dev/null || true
}

# sha256_16 prints a short hash of stdin (for evidence without bodies).
sha256_16() { sha256sum | cut -c1-16; }

# pass/skip report a verdict and (for skip) exit successfully; only real
# failures may exit nonzero, via die.
pass() { log "PASS: $*"; }
skip() { log "SKIP: $*"; exit 0; }

# --- Matrix-side reads (iamb + client-server API) -------------------------
#
# Read-only helpers. `event_meta` and friends go through the iamb daemon, so
# E2EE portal events come back decrypted. `matrix_api_*` talk to the
# homeserver with the access token already stored in the iamb profile session
# file: the token is read and used inside the IAMB_SSH shell only, never
# printed, and never passed on this machine's command line.
#
# Bodies are never printed: callers get lengths, hashes, mxc URIs and event
# ids instead.

: "${IAMB_SESSION_JSON:=$HOME/.local/share/iamb/profiles/$IAMB_PROFILE/session.json}"

iamb_version() {
	iamb_cmd 'version' 2>/dev/null | head -n 1
}

# iamb_message_json ROOM EVENT_ID prints the decrypted source JSON of one event.
iamb_message_json() {
	local room="$1"
	local event="$2"
	iamb_cmd "message '$room' '$event'"
}

# event_meta ROOM EVENT_ID prints body-free metadata for one event: ids,
# sender, type, msgtype, media url/size, reply relation and body length/hash.
event_meta() {
	local room="$1"
	local event="$2"
	local json body_hash
	json=$(iamb_message_json "$room" "$event") || return 1
	body_hash=$(jq -r '.content.body // ""' <<<"$json" | sha256_16)
	jq -c --arg bh "$body_hash" '{
		event_id, sender, type, ts: .origin_server_ts,
		msgtype: (.content.msgtype // null),
		url: (.content.url // .content.file.url // null),
		has_media: (.content.url != null or .content.file != null),
		size: (.content.info.size // null),
		body_len: ((.content.body // "") | length),
		body_sha256_16: $bh,
		relates_to: (.content["m.relates_to"] // null),
		new_content_body_len: ((.content["m.new_content"].body // "") | length),
		redacts: (.redacts // null)
	}' <<<"$json"
}

# mxc_plain_hash ROOM EVENT_ID prints the media of one event as
# "plain_size=<n> plain_sha256_16=<h> embedded_sha256_16=<h|none>
#  embedded_matches_plain=<yes|no|n/a> cipher_sha256_16=<h>".
# Encrypted (E2EE) media is decrypted with the key/iv carried by the event;
# some clients store the embedded hashes over the *encrypted* upload, so the
# authoritative comparison for the tests is plain_sha256_16 against the local
# fixture. The event JSON and the access token never leave IAMB_SSH, and only
# sizes/hashes are printed.
mxc_plain_hash() {
	require_env IAMB_SSH
	local room="$1"
	local event="$2"
	local remote_ev rc=0
	remote_ev=$(ssh "$IAMB_SSH" mktemp)
	iamb_message_json "$room" "$event" | ssh "$IAMB_SSH" "cat > '$remote_ev'" || rc=$?
	if [ "$rc" -eq 0 ]; then
		ssh "$IAMB_SSH" \
			"TOKEN=\$(jq -r .access_token '$IAMB_SESSION_JSON'); HS=\$(jq -r .homeserver '$IAMB_SESSION_JSON'); HS=\${HS%/}; EV_FILE='$remote_ev'; export TOKEN HS EV_FILE; bash -s" <<'REMOTE' || rc=$?
set -euo pipefail
ev=$(cat "$EV_FILE")
b64d() {
	local v="$1"
	local pad=$(( (4 - ${#v} % 4) % 4 ))
	if [ "$pad" -gt 0 ]; then
		v="$v$(printf '%*s' "$pad" '' | tr ' ' '=')"
	fi
	# Accept both standard and URL-safe alphabets (Matrix JWKs vary).
	printf '%s' "$v" | tr '_-' '/+' | base64 -d
}
url=$(jq -r '.content.url // .content.file.url // empty' <<<"$ev")
if [ -z "$url" ]; then
	echo "no-media" >&2
	exit 1
fi
expected=$(jq -r '.content.file.hashes.sha256 // empty' <<<"$ev")
key=$(jq -r 'if (.content.file.key | type) == "object" then .content.file.key.k // empty else .content.file.key // empty end' <<<"$ev")
iv=$(jq -r '.content.file.iv // empty' <<<"$ev")
rest=${url#mxc://}
f=$(mktemp)
p=$(mktemp)
ok=0
for path in /_matrix/client/v1/media/download /_matrix/media/v3/download; do
	if curl -fsS --max-time 60 -H "Authorization: Bearer $TOKEN" -o "$f" "$HS$path/$rest"; then
		ok=1
		break
	fi
done
if [ "$ok" != 1 ]; then
	rm -f "$f" "$p"
	echo "media download failed" >&2
	exit 1
fi
if [ -n "$key" ]; then
	keyhex=$(b64d "$key" | xxd -p | tr -d '\n')
	ivhex=$(b64d "$iv" | xxd -p | tr -d '\n')
	openssl enc -d -aes-256-ctr -K "$keyhex" -iv "$ivhex" -in "$f" -out "$p"
else
	cp "$f" "$p"
fi
size=$(wc -c < "$p")
h=$(sha256sum "$p" | cut -c1-16)
ch=$(sha256sum "$f" | cut -c1-16)
exp16=""
if [ -n "$expected" ]; then
	exp16=$(b64d "$expected" 2>/dev/null | xxd -p | tr -d '\n' | cut -c1-16) || exp16=""
fi
match=n/a
if [ -n "$exp16" ]; then
	if [ "$exp16" = "$h" ]; then match=yes; else match=no; fi
fi
printf 'plain_size=%s plain_sha256_16=%s embedded_sha256_16=%s embedded_matches_plain=%s cipher_sha256_16=%s\n' \
	"$size" "$h" "${exp16:-none}" "$match" "$ch"
rm -f "$f" "$p"
REMOTE
	fi
	ssh "$IAMB_SSH" "rm -f '$remote_ev'" || true
	return "$rc"
}

# matrix_session_field JQ_EXPR reads one field from the iamb session file on
# IAMB_SSH. Only non-secret fields (user_id, homeserver, device_id) should be
# printed; access_token must stay inside the remote shell.
matrix_session_field() {
	require_env IAMB_SSH
	local expr="$1"
	local sess
	sess=$(remote_path "$IAMB_SESSION_JSON")
	ssh "$IAMB_SSH" "jq -r '$expr // empty' \"$sess\""
}

# matrix_api_get PATH performs an authenticated GET on the homeserver.
# (`api_path`, not `path`: in zsh `path` is tied to PATH.)
matrix_api_get() {
	require_env IAMB_SSH
	local api_path="$1"
	local sess
	sess=$(remote_path "$IAMB_SESSION_JSON")
	ssh "$IAMB_SSH" "TOKEN=\$(jq -r .access_token \"$sess\"); HS=\$(jq -r .homeserver \"$sess\"); HS=\${HS%/}; curl -fsS --max-time 30 -H \"Authorization: Bearer \$TOKEN\" \"\$HS$api_path\""
}

# matrix_api_send ROOM TYPE TXN reads a JSON body on stdin and PUTs it as an
# event of TYPE into ROOM using TXN as the transaction id.
matrix_api_send() {
	require_env IAMB_SSH
	local room="$1"
	local type="$2"
	local txn="$3"
	local sess
	sess=$(remote_path "$IAMB_SESSION_JSON")
	ssh "$IAMB_SSH" "TOKEN=\$(jq -r .access_token \"$sess\"); HS=\$(jq -r .homeserver \"$sess\"); HS=\${HS%/}; curl -fsS --max-time 30 -X PUT -H \"Authorization: Bearer \$TOKEN\" -H 'Content-Type: application/json' --data-binary @- \"\$HS/_matrix/client/v3/rooms/$room/send/$type/$txn\""
}

# matrix_api_redact ROOM EVENT TXN [REASON] redacts one event.
matrix_api_redact() {
	require_env IAMB_SSH
	local room="$1"
	local event="$2"
	local txn="$3"
	local reason="${4:-bridge validation harness}"
	local sess event_url
	sess=$(remote_path "$IAMB_SESSION_JSON")
	# Event ids start with $; escape it so the remote shell does not read it
	# as a variable inside the URL.
	event_url=$(printf '%s' "$event" | sed 's/\$/\\$/g')
	ssh "$IAMB_SSH" "TOKEN=\$(jq -r .access_token \"$sess\"); HS=\$(jq -r .homeserver \"$sess\"); HS=\${HS%/}; curl -fsS --max-time 30 -X PUT -H \"Authorization: Bearer \$TOKEN\" -H 'Content-Type: application/json' --data-binary @- \"\$HS/_matrix/client/v3/rooms/$room/redact/$event_url/$txn\"" <<<"$(jq -cn --arg r "$reason" '{reason:$r}')"
}

# room_events_since ROOM SINCE_MS [LIMIT] prints events at or after SINCE_MS
# (epoch ms), oldest first, as metadata-only ndjson. Encrypted portal events
# surface as m.room.encrypted here; decrypt them individually with
# iamb_message_json/event_meta.
room_events_since() {
	local room="$1"
	local since_ms="$2"
	local limit="${3:-30}"
	matrix_api_get "/_matrix/client/v3/rooms/$room/messages?dir=b&limit=$limit" |
		jq -c --argjson since "$since_ms" \
			'[.chunk[] | select(.origin_server_ts >= $since)] | sort_by(.origin_server_ts) | .[] |
			 {event_id, sender, type, ts: .origin_server_ts, redacts: (.redacts // null)}'
}

# room_event_ids ROOM [LIMIT] prints the newest event ids first (one per line).
room_event_ids() {
	local room="$1"
	local limit="${2:-20}"
	matrix_api_get "/_matrix/client/v3/rooms/$room/messages?dir=b&limit=$limit" |
		jq -r '.chunk[].event_id'
}

# room_event_ids_hash ROOM [LIMIT] prints a sha256_16 of the ordered newest
# event ids; cheap "nothing changed" evidence for restart tests.
room_event_ids_hash() {
	room_event_ids "$1" "${2:-20}" | sha256_16
}

# mxc_hash MXC prints "size=<n> sha256_16=<h>" for a mxc:// URI by downloading
# it through the homeserver on IAMB_SSH. The file never leaves that host.
mxc_hash() {
	require_env IAMB_SSH
	local mxc="$1"
	local rest="${mxc#mxc://}"
	local sess
	sess=$(remote_path "$IAMB_SESSION_JSON")
	ssh "$IAMB_SSH" "set -e; TOKEN=\$(jq -r .access_token \"$sess\"); HS=\$(jq -r .homeserver \"$sess\"); HS=\${HS%/}; F=\$(mktemp); ok=0; for media_path in /_matrix/client/v1/media/download /_matrix/media/v3/download; do if curl -fsS --max-time 60 -H \"Authorization: Bearer \$TOKEN\" -o \"\$F\" \"\$HS\$media_path/$rest\"; then ok=1; break; fi; done; if [ \"\$ok\" != 1 ]; then rm -f \"\$F\"; echo 'media download failed' >&2; exit 1; fi; printf 'size=%s sha256_16=%s\n' \"\$(wc -c < \"\$F\")\" \"\$(sha256sum \"\$F\" | cut -c1-16)\"; rm -f \"\$F\""
}

# room_uploads ROOM [LIMIT] lists recent media events (event id, age, sender,
# kind and file name) through iamb.
room_uploads() {
	local room="$1"
	local limit="${2:-10}"
	iamb_cmd "room uploads '$room' --limit '$limit'"
}

# newest_phone_message ROOM [LINES] prints one meta line (event id, sender,
# timestamp, body length) for the newest message in ROOM that did not come
# from the iamb user or the bridge bot, i.e. the newest message the phone (or
# another XMPP contact) sent. Bodies are never printed.
newest_phone_message() {
	require_env IAMB_SSH
	local room="$1"
	local lines="${2:-60}"
	local me dir
	me=$(matrix_session_field .user_id)
	dir=$(remote_path "$IAMB_ARCHIVE_DIR")
	ssh "$IAMB_SSH" "tail -n '$lines' \"$dir/${room#!}\" 2>/dev/null" |
		jq -c --arg me "$me" \
			'select(.sender != $me and (.sender | test("jmpchatbot") | not)) |
			 {event_id, sender, ts: .timestamp, body_len: ((.body // "") | length)}' 2>/dev/null |
		tail -n 1 || true
}

# newest_own_event ROOM SINCE_MS JQ_FILTER prints the meta line of the newest
# event in ROOM at/after SINCE_MS that was sent by the iamb user and matches
# JQ_FILTER (which is evaluated with $me bound to the user id). Used when the
# iamb send summary does not list the sent event (reactions, attachments).
newest_own_event() {
	local room="$1"
	local since_ms="$2"
	local filter="$3"
	local me cand ev meta best="" cands
	me=$(matrix_session_field .user_id)
	mapfile -t cands < <(room_events_since "$room" "$since_ms" 15 || true)
	for cand in "${cands[@]}"; do
		[ -n "$cand" ] || continue
		ev=$(jq -r '.event_id' <<<"$cand")
		meta=$(event_meta "$room" "$ev" 2>/dev/null || true)
		[ -n "$meta" ] || continue
		if jq -e --arg me "$me" "$filter" <<<"$meta" >/dev/null 2>&1; then
			best="$meta"
		fi
	done
	[ -n "$best" ] && printf '%s\n' "$best"
	return 0
}

# --- Matrix sends (iamb) --------------------------------------------------

# send_attach_wait sends a file to ROOM and waits for a different-sender
# reply; the file must be readable on IAMB_SSH (copy it there first).
send_attach_wait() {
	require_env IAMB_SSH
	local room="$1"
	local file="$2"
	local timeout="${3:-$VALIDATE_TIMEOUT}"
	ssh "$IAMB_SSH" \
		"$IAMB_BIN -P '$IAMB_PROFILE' --no-ipc send --room '$room' --attach '$file' --wait --timeout '$timeout' --reply-mode different-sender --wait-output json"
}

# send_reply_wait replies to TARGET in ROOM with TEXT (stdin) and waits.
send_reply_wait() {
	require_env IAMB_SSH
	local room="$1"
	local target="$2"
	local text="$3"
	local timeout="${4:-$VALIDATE_TIMEOUT}"
	printf '%s' "$text" | ssh "$IAMB_SSH" \
		"$IAMB_BIN -P '$IAMB_PROFILE' --no-ipc send --room '$room' --reply '$target' --stdin --wait --timeout '$timeout' --reply-mode different-sender --wait-output json"
}

# react_wait sends a reaction to TARGET in ROOM and waits briefly.
react_wait() {
	require_env IAMB_SSH
	local room="$1"
	local target="$2"
	local emoji="$3"
	local timeout="${4:-15}"
	ssh "$IAMB_SSH" \
		"$IAMB_BIN -P '$IAMB_PROFILE' --no-ipc send --room '$room' --react-to '$target' --text '$emoji' --wait --timeout '$timeout' --reply-mode different-sender --wait-output json"
}

# send_expect_ok LABEL validates the send half of a wait JSON on stdin: no
# send failures, no errors. A missing reply is not an error here. Prints the
# JSON plus the sent event ids.
send_expect_ok() {
	local label="$1"
	local json
	json=$(cat)
	if ! jq -e '.messages.failures == 0 and (.errors | length) == 0' <<<"$json" >/dev/null; then
		jq -c '{sent: .messages.sent, failures: .messages.failures, errors: .errors}' <<<"$json" >&2
		die "$label: send reported failures"
	fi
	local ids
	ids=$(jq -r '[.messages.sent_events[].event_id] | join(",")' <<<"$json")
	log "$label: sent (event_ids=$ids)"
	printf '%s' "$json"
}

# --- Physical handsets (read-only) ---------------------------------------
#
# Handset SMS access goes through `content query` on an adb-attached device.
# The device command is piped to `adb shell` over stdin, so it passes the
# local shell (and an optional SSH hop) as plain text with no quoting layers.
# These helpers only ever read; never insert, update or delete SMS rows.

# adb_shell SERIAL CMD runs one shell command on the device over adb.
adb_shell() {
	local serial="$1"
	local cmd="$2"
	local runner="shell"
	[ "${PHONE_USE_SU:-0}" = "1" ] && runner="shell su"
	if [ -n "${PHONE_ADB_SSH:-}" ]; then
		printf '%s\n' "$cmd" | ssh "$PHONE_ADB_SSH" "adb -s '$serial' $runner"
	else
		# Word splitting is intended: "shell" or "shell su".
		# shellcheck disable=SC2086
		printf '%s\n' "$cmd" | adb -s "$serial" $runner
	fi
}

# phone_sms_rows SERIAL SENDER prints raw inbox rows (address, date, body)
# for messages whose sender address contains SENDER. Callers must keep the
# body column out of logs: match tokens and hash bodies, never print them.
phone_sms_rows() {
	local serial="$1"
	local sender="$2"
	local digits="${sender#+}"
	adb_shell "$serial" "content query --uri content://sms/inbox --projection address,date,body --where \"address LIKE '%${digits}%'\" --sort \"date DESC\""
}

# phone_sms_rows_since SERIAL SINCE_MS prints inbox rows received at or after
# SINCE_MS (epoch milliseconds), newest first. Used when the sender address
# of the expected SMS is not known in advance.
phone_sms_rows_since() {
	local serial="$1"
	local since_ms="$2"
	adb_shell "$serial" "content query --uri content://sms/inbox --projection address,date,body --where \"date >= ${since_ms}\" --sort \"date DESC\""
}

# --- Physical handsets: mmssms.db reads (read-only) ----------------------
#
# The content provider exposes SMS rows but not MMS payloads; when a handset
# allows root adb (Magisk, or userdebug/eng builds) the SQLite database can be
# queried directly for pdu/part metadata and the part files hashed in place.
# These helpers only ever read; never insert, update or delete rows.
#
# SQL strings passed to phone_db_query must not contain double quotes (the
# device shell parses the whole line). Single quotes for SQL literals are
# fine.

: "${PHONE_DB:=/data/data/com.android.providers.telephony/databases/mmssms.db}"

# phone_db_query SERIAL SQL runs one SQLite statement via /system/bin/sqlite3.
phone_db_query() {
	local serial="$1"
	local sql="$2"
	adb_shell "$serial" "sqlite3 $PHONE_DB \"$sql\""
}

# phone_mms_rows_since SERIAL SINCE_S prints pdu rows (msg_box, date,
# content type, thread, subscription, size) at or after SINCE_S seconds,
# newest first, pipe-separated. Column count is fixed by the query.
phone_mms_rows_since() {
	local serial="$1"
	local since_s="$2"
	phone_db_query "$serial" "select _id||'|'||msg_box||'|'||date||'|'||coalesce(ct_t,'')||'|'||thread_id||'|'||sub_id||'|'||m_size from pdu where date >= ${since_s} order by date desc"
}

# phone_mms_parts SERIAL PDU_ID prints part rows for one pdu: part id, seq,
# content type, name, path length and path. Paths are local to the handset.
phone_mms_parts() {
	local serial="$1"
	local pdu="$2"
	phone_db_query "$serial" "select _id||'|'||seq||'|'||ct||'|'||coalesce(name,'')||'|'||length(coalesce(_data,''))||'|'||coalesce(_data,'') from part where mid=${pdu} order by seq"
}

# phone_mms_addresses SERIAL PDU_ID prints "count" followed by one hashed
# participant list, so group membership can be asserted without echoing
# numbers row by row.
phone_mms_addresses() {
	local serial="$1"
	local pdu="$2"
	local numbers
	numbers=$(phone_db_query "$serial" "select group_concat(address,',') from addr where msg_id=${pdu}" | tr -d '\r')
	printf '%s %s\n' "$(printf '%s' "$numbers" | awk -F, '{print NF}')" "$(printf '%s' "$numbers" | sha256_16)"
}

# phone_sms_rows_db SERIAL SINCE_MS prints sent/received sms rows at or after
# SINCE_MS from the database (id, type, date, thread), newest first.
phone_sms_rows_db() {
	local serial="$1"
	local since_ms="$2"
	phone_db_query "$serial" "select _id||'|'||type||'|'||date||'|'||thread_id from sms where date >= ${since_ms} order by date desc"
}

# phone_sms_body_hash SERIAL SMS_ID hashes one sms body without printing it.
phone_sms_body_hash() {
	local serial="$1"
	local sms_id="$2"
	phone_db_query "$serial" "select body from sms where _id=${sms_id}" | sha256_16
}

# phone_sms_body_prefix SERIAL SMS_ID PREFIX prints "yes"/"no" (plus a hash),
# comparing the stored body against a literal prefix. Use for formatting
# expectations ("* " edits, "> " reply quotes) without logging the body.
phone_sms_body_prefix() {
	local serial="$1"
	local sms_id="$2"
	local prefix="$3"
	local body
	body=$(phone_db_query "$serial" "select body from sms where _id=${sms_id}" | tr -d '\r')
	local hash
	hash=$(printf '%s' "$body" | sha256_16)
	case "$body" in
	"$prefix"*) printf 'yes len=%s sha256_16=%s\n' "${#body}" "$hash" ;;
	*) printf 'no len=%s sha256_16=%s\n' "${#body}" "$hash" ;;
	esac
}

# phone_file_info SERIAL PATH prints "size=<n> sha256_16=<h>" for a file on
# the handset (used for MMS part payloads and pushed fixtures).
phone_file_info() {
	local serial="$1"
	local path="$2"
	adb_shell "$serial" "sz=\$(wc -c < '$path' 2>/dev/null || echo 0); printf 'size=%s sha256_16=%s\n' \"\$sz\" \"\$(sha256sum '$path' 2>/dev/null | cut -c1-16)\""
}

# phone_push_file SERIAL LOCAL REMOTE pushes a local file to the handset,
# hopping through PHONE_ADB_SSH when the device is attached there.
phone_push_file() {
	local serial="$1"
	local src="$2"
	local dst="$3"
	if [ -n "${PHONE_ADB_SSH:-}" ]; then
		local tmp
		tmp=$(ssh "$PHONE_ADB_SSH" mktemp)
		scp -q "$src" "$PHONE_ADB_SSH:$tmp"
		ssh "$PHONE_ADB_SSH" "adb -s '$serial' push '$tmp' '$dst' >/dev/null; rc=\$?; rm -f '$tmp'; exit \$rc"
	else
		adb -s "$serial" push "$src" "$dst" >/dev/null
	fi
}

# --- Physical handsets: intent control (Fossify Messages bridge build) ----
#
# The bridge build of Fossify Messages exposes token-gated control
# broadcasts (see the fossify-messages docs, "Bridge mode"). The control
# token is read from the app's own preferences on the device and used inside
# the device shell only; it is never printed. These helpers send messages
# (SEND) or read status (STATUS); both are explicitly invoked per test, so
# the "read-only" rule above still holds for the DB helpers.

: "${PHONE_CONTROL_PKG:=org.fossify.messages.bridge.debug}"

# phone_control_broadcast SERIAL ACTION [EXTRA...] prints the JSON result
# data of a control broadcast, or nothing when the app returned none.
phone_control_broadcast() {
	local serial="$1"
	local action="$2"
	shift 2
	local extras=""
	local extra
	for extra in "$@"; do
		extras="$extras $extra"
	done
	local pkg="$PHONE_CONTROL_PKG"
	local cmd="P=/data/data/$pkg/shared_prefs/Prefs.xml; TOKEN=\$(sed -n 's/.*<string name=\"control_token\">\([^<]*\)<.*/\1/p' \"\$P\"); OUT=\$(am broadcast -n $pkg/org.fossify.messages.control.ControlReceiver -a org.fossify.messages.bridge.control.$action --es control_token \"\$TOKEN\"$extras 2>&1); printf '%s\n' \"\$OUT\" | sed -n 's/^.*data=\"\(.*\)\"\$/\1/p'"
	adb_shell "$serial" "$cmd"
}

# phone_control_status SERIAL prints the STATUS JSON (never contains content).
phone_control_status() {
	phone_control_broadcast "$1" STATUS
}

# phone_control_send SERIAL TO TEXT [FILE] performs one group-aware send
# (SMS without FILE, MMS with it) and prints the JSON result. Bump the send
# budget deliberately: this is a real SMS/MMS.
phone_control_send() {
	local serial="$1"
	local to="$2"
	local text="$3"
	local file="${4:-}"
	# The quotes are for the device shell that parses the command line.
	# shellcheck disable=SC2089,SC2090
	local extras="--es to '$to' --es text '$text'"
	[ -n "$file" ] && extras="$extras --es file '$file'"
	# shellcheck disable=SC2090
	phone_control_broadcast "$serial" SEND $extras
}

# --- Bridge service control (restart tests) ------------------------------
#
# Only meaningful when the harness may control the host that runs the bridge
# container. Keep JMP_BRIDGE_SSH unset for read-only runs; tests that need a
# restart SKIP instead of guessing.

: "${JMP_BRIDGE_SERVICE:=matrix-mautrix-jmpchat}"
: "${JMP_BRIDGE_CONTAINER:=matrix-mautrix-jmpchat}"

# bridge_restart restarts the bridge service on JMP_BRIDGE_SSH. Some
# deployments hit a docker name race on restart (the old container is removed
# asynchronously); a transient failure is retried once, and bridge_wait_running
# is the authority on whether the bridge actually came back.
bridge_restart() {
	require_env JMP_BRIDGE_SSH
	log "restarting $JMP_BRIDGE_SERVICE on $JMP_BRIDGE_SSH"
	if ! ssh "$JMP_BRIDGE_SSH" "systemctl restart '$JMP_BRIDGE_SERVICE'"; then
		log "restart reported a failure (known transient docker-name race); retrying once"
		ssh "$JMP_BRIDGE_SSH" "sleep 3; systemctl start '$JMP_BRIDGE_SERVICE' || true"
	fi
}

# bridge_wait_running waits until the container is running again and prints
# the container start timestamp.
bridge_wait_running() {
	require_env JMP_BRIDGE_SSH
	local deadline=$(( $(date +%s) + ${BRIDGE_RESTART_TIMEOUT:-90} ))
	while :; do
		local state
		state=$(ssh "$JMP_BRIDGE_SSH" "docker inspect -f '{{.State.Running}}' '$JMP_BRIDGE_CONTAINER' 2>/dev/null" || true)
		if [ "$state" = "true" ]; then
			ssh "$JMP_BRIDGE_SSH" "docker inspect -f 'container_running={{.State.Running}} started_at={{.State.StartedAt}}' '$JMP_BRIDGE_CONTAINER'"
			return 0
		fi
		[ "$(date +%s)" -ge "$deadline" ] && die "bridge container did not come back within ${BRIDGE_RESTART_TIMEOUT:-90}s"
		sleep 3
	done
}

# bridge_wait_xmpp waits until the bridge logs a fresh XMPP health check
# after SINCE_EPOCH (seconds; 0 accepts the newest line) and prints the
# matching log line with the message body stripped down to its fields.
bridge_wait_xmpp() {
	require_env JMP_BRIDGE_SSH
	local since_epoch="${1:-0}"
	local deadline=$(( $(date +%s) + ${BRIDGE_RESTART_TIMEOUT:-90} ))
	while :; do
		local line
		line=$(ssh "$JMP_BRIDGE_SSH" "docker logs --since '$since_epoch' '$JMP_BRIDGE_CONTAINER' 2>&1 | grep 'XMPP runtime health check succeeded' | tail -n 1" || true)
		if [ -n "$line" ]; then
			printf '%s\n' "$line" | sed 's/\x1b\[[0-9;]*m//g' | grep -oE 'health check succeeded.*' | head -c 200
			printf '\n'
			return 0
		fi
		[ "$(date +%s)" -ge "$deadline" ] && return 1
		sleep 3
	done
}

# bridge_log_grep PATTERN [COUNT] counts matching bridge log lines (bodies
# never appear in the lines selected by the tests' patterns).
bridge_log_grep() {
	require_env JMP_BRIDGE_SSH
	local pattern="$1"
	ssh "$JMP_BRIDGE_SSH" "docker logs '$JMP_BRIDGE_CONTAINER' 2>&1 | grep -cE '$pattern' || true"
}

# bridge_log_grep_since PATTERN SINCE_EPOCH counts matching bridge log lines
# from SINCE_EPOCH (seconds) onward.
bridge_log_grep_since() {
	require_env JMP_BRIDGE_SSH
	local pattern="$1"
	local since_epoch="$2"
	ssh "$JMP_BRIDGE_SSH" "docker logs --since '$since_epoch' '$JMP_BRIDGE_CONTAINER' 2>&1 | grep -cE '$pattern' || true"
}

# bridge_db_query SQL runs one read-only SQL statement against the bridge
# database through the database container on JMP_BRIDGE_SSH. Keep SQL free of
# double quotes and dollar signs (it is executed inside a shell string).
: "${JMP_BRIDGE_PG_CONTAINER:=matrix-postgres}"
: "${JMP_BRIDGE_PG_USER:=matrix}"
: "${JMP_BRIDGE_PG_DB:=matrix_bridge_mautrix_jmpchat}"

bridge_db_query() {
	require_env JMP_BRIDGE_SSH
	local sql="$1"
	ssh "$JMP_BRIDGE_SSH" "docker exec '$JMP_BRIDGE_PG_CONTAINER' psql -U '$JMP_BRIDGE_PG_USER' -d '$JMP_BRIDGE_PG_DB' -tAc \"$sql\""
}
