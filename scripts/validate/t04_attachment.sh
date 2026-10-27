#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T04 — attachments (image in both directions). SKELETON ONLY.
#
# This test is deliberately not automated yet: a full round-trip needs a
# cooperating human on a handset (to send one image and to confirm the
# received one), and the phone side of MMS is not reachable through the
# read-only adb helpers used by T02. Nothing is sent or received by this
# script; without RUN=1 it just logs a dry-run line.
#
# Planned manual procedure (evidence = event ids, mxc URIs, byte sizes and
# sha256_16 prefixes; media bytes and captions are never logged):
#
#   Matrix -> phone:
#     1. LOCAL_HASH=$(sha256sum /tmp/opencode/t04-image.png | cut -c1-16)
#     2. resolve the room for JMP_NUMBER (room_for_number) and send:
#          ssh "$IAMB_SSH" "$IAMB_BIN -P $IAMB_PROFILE send \
#            --room '$ROOM' --attach /tmp/opencode/t04-image.png"
#     3. the human on the handset confirms the image arrived (correct file,
#        not a placeholder); MMS payloads are not exposed via
#        content://sms, so a byte hash cannot be read back with adb today.
#     4. record: sent event id, local sha256_16.
#
#   Phone -> Matrix:
#     1. the human sends the same image from the handset to the JMP number.
#     2. find the bridged event in the iamb archive (IDs and content
#        metadata only):
#          ssh "$IAMB_SSH" "tail -n 20 '$IAMB_ARCHIVE_DIR/<room-without-!>.ndjson'" |
#            jq -c 'select(.type=="m.room.message") |
#              {event_id, sender, msgtype: .content.msgtype,
#               url: .content.url, size: .content.info.size}'
#     3. download the mxc:// media through the homeserver, hash the bytes:
#          sha256sum downloaded-file | cut -c1-16
#     4. pass only when the two hashes match, or when a documented re-encode
#        (matrix-media-repo, bridge thumbnailing) explains a mismatch; a
#        visual check alone is a partial pass and must say so.
#
# Why auto-testing is hard:
#   * `content query` only exposes SMS rows; MMS payloads live in the MMS
#     provider behind a download step and are not readable with the adb
#     helpers in lib.sh;
#   * one direction must be originated by a human;
#   * media may be re-encoded between Matrix and MMS, so byte equality is
#     only guaranteed when no re-encode happens.
#
# Usage:
#   scripts/validate/t04_attachment.sh          # dry run (prints nothing else)
#   RUN=1 scripts/validate/t04_attachment.sh    # prints the manual procedure

set -euo pipefail
LIB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$LIB_DIR/lib.sh"

if [ "${RUN:-0}" != "1" ]; then
	log "dry run: the t04 attachment round-trip is manual; nothing sent or received"
	log "re-run with RUN=1 to print the manual procedure"
	exit 0
fi

require_jmp_env

cat <<EOF
T04 attachment round-trip — manual procedure (this script sent nothing)

Requirements:
  * a human on a handset to send one image and confirm the received one
  * a local image file, e.g. /tmp/opencode/t04-image.png
  * iamb profile '$IAMB_PROFILE' on $IAMB_SSH, JMP number $JMP_NUMBER

Matrix -> phone:
  1. LOCAL_HASH=\$(sha256sum /tmp/opencode/t04-image.png | cut -c1-16)
  2. resolve the room id for $JMP_NUMBER first (full !id only, see
     docs/validation.md) and keep it as \$ROOM:
     ssh $IAMB_SSH "$IAMB_BIN -P $IAMB_PROFILE send --room \"\$ROOM\" --attach /tmp/opencode/t04-image.png"
  3. ask the human to confirm the image arrived intact.
  4. record: sent event id, local sha256_16=\$LOCAL_HASH.

Phone -> Matrix:
  1. ask the human to send the same image from the handset.
  2. inspect new archive events (metadata only, never bodies):
       ssh $IAMB_SSH "tail -n 20 '\$IAMB_ARCHIVE_DIR/<room-without-!>.ndjson'" |
         jq -c 'select(.type=="m.room.message") | {event_id, sender, msgtype: .content.msgtype, url: .content.url, size: .content.info.size}'
  3. download the mxc:// media and hash it: sha256sum <file> | cut -c1-16.
  4. pass when hashes match, or document the re-encode that explains a mismatch.

Evidence rules: ids, mxc URIs, byte sizes and sha256_16 prefixes only —
never media bytes, captions or other message bodies.
EOF

log "T04 instructions printed; this script never sends attachments"
