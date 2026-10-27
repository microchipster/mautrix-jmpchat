#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# T09 — read receipts: capability probe, no messages sent.
#
# Matrix read receipt -> phone: the connector implements
# ReadReceiptHandlingNetworkAPI and relays Matrix read receipts to XMPP as
# XEP-0333 chat markers; a carrier SMS has no read state to flip, and the
# Cheogram SMS gateway does not surface chat markers as a new message, so
# there is nothing observable in the handset databases for a PSTN number.
#
# Inbound delivery receipt -> Matrix: the connector asks for XEP-0184
# receipts (simple_config.send_delivery_receipts) and maps incoming receipts
# and markers back to Matrix read receipts. Whether a handset/carrier ever
# generates one that survives the gateway is outside the bridge.
#
# The test therefore *documents* the capability: it records the code paths
# (via the repository), probes the handset databases for read-state columns,
# counts receipt-related lines in the bridge log when JMP_BRIDGE_SSH is set,
# and SKIPs when nothing is observable, instead of pretending to prove it.
#
# Usage:
#   JMP_NUMBER=+15551234567 PHONE_NUMBER=+15557654321 PHONE_SERIAL=<adb-serial> \
#   scripts/validate/t09_receipts.sh
#
# Optional:
#   JMP_BRIDGE_SSH  bridge host, enables the log probe (else that probe skips)
#   PHONE_* / IAMB_*  see lib.sh

set -euo pipefail
LIB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$LIB_DIR/lib.sh"

require_jmp_env
require_env PHONE_NUMBER PHONE_SERIAL

assert_room_count "$PHONE_NUMBER" 1
room=$(room_for_number "$PHONE_NUMBER")
[ -n "$room" ] || die "no portal room for $PHONE_NUMBER"
log "portal room: $room"

log "t09 code: connector implements ReadReceiptHandlingNetworkAPI (connector/xmpp_receipts.go) and requests XEP-0184 receipts on outbound chat stanzas"

# Handset read-state columns for the messages this bridge sent: the SMS
# provider keeps `seen`/`read` flags; they reflect *local* handset state, not
# a receipt that travelled back over the carrier.
mms_state=$(phone_db_query "$PHONE_SERIAL" "select count(*)||' inbox rows, '||coalesce(sum(case when read=1 then 1 else 0 end),0)||' marked read' from pdu where msg_box = 1" | tr -d '\r')
sms_state=$(phone_db_query "$PHONE_SERIAL" "select count(*)||' inbox rows' from sms where type = 1" | tr -d '\r')
log "t09 phone: pdu: $mms_state; sms: $sms_state (local state only; no carrier receipt is passed to the bridge)"
log "t09 phone: verdict for a PSTN number — nothing a Matrix read receipt can change on the handset, nothing the handset sends that the bridge could map back"

if [ -n "${JMP_BRIDGE_SSH:-}" ]; then
	receipt_lines=$(bridge_log_grep 'receipt|marker|XEP-0184|XEP-0333' || true)
	log "t09 bridge: log lines mentioning receipts/markers (whole container log): ${receipt_lines:-0}"
else
	log "t09 bridge: log probe skipped (set JMP_BRIDGE_SSH to count receipt-related bridge log lines)"
fi

skip "read receipts are not observable on a PSTN handset: SMS/MMS carry no read marker from the phone and the bridge relays receipts only as XEP-0333/0184 to XMPP clients; capability documented, nothing to fail"
