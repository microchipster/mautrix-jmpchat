#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T01 — smoke test: the iamb daemon is reachable, the JMP number resolves to a
# bridged room, and the room carries the bridge marker. Read-only: no messages
# are sent.
#
# Usage:
#   JMP_NUMBER=+15551234567 scripts/validate/t01_smoke.sh
#
# Environment: see scripts/validate/lib.sh.

set -euo pipefail
LIB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$LIB_DIR/lib.sh"

require_jmp_env

log "checking iamb daemon on $IAMB_SSH ($IAMB_PROFILE)"
rooms=$(rooms_json)
count=$(jq '.matches | length' <<<"$rooms")
if [ "$count" -eq 0 ]; then
	die "no rooms returned — is the iamb TUI running on $IAMB_SSH?"
fi
log "rooms visible: $count"

log "resolving room for $JMP_NUMBER"
room=$(room_for_number "$JMP_NUMBER")
if [ -z "$room" ]; then
	die "no room found for $JMP_NUMBER (has the bridge created it yet?)"
fi
log "room: $room"

assert_bridge_room "$room"

log "T01 smoke OK"
