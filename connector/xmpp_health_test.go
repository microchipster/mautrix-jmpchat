// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"testing"
	"time"
)

func TestServerIdleTracking(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	if idle := nc.serverIdleFor(); idle < xmppIdlePingThreshold {
		t.Errorf("fresh client should be treated as idle, got %s", idle)
	}
	nc.recordServerActivity()
	if idle := nc.serverIdleFor(); idle >= xmppIdlePingThreshold {
		t.Errorf("after activity the stream should not count as idle, got %s", idle)
	}
}

func TestHealthPingThresholds(t *testing.T) {
	t.Parallel()
	if xmppIdlePingThreshold != 90*time.Second {
		t.Errorf("idle ping threshold should be 90s, got %s", xmppIdlePingThreshold)
	}
	if xmppSelfPingInterval <= xmppIdlePingThreshold {
		t.Errorf("self-ping interval (%s) should exceed the idle threshold (%s)", xmppSelfPingInterval, xmppIdlePingThreshold)
	}
}
