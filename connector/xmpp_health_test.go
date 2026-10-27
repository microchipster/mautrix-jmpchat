// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"testing"
	"time"

	"gosrc.io/xmpp/stanza"
)

func TestClassifySelfPingAnswer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		iqType   stanza.StanzaType
		wantOK   bool
		loopback bool
	}{
		{name: "result pong", iqType: stanza.IQTypeResult, wantOK: true},
		{name: "get loopback", iqType: stanza.IQTypeGet, wantOK: true, loopback: true},
		{name: "error answer", iqType: stanza.IQTypeError},
		{name: "set answer", iqType: stanza.IQTypeSet},
		{name: "empty type", iqType: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ok, loopback := classifySelfPingAnswer(tt.iqType)
			if ok != tt.wantOK || loopback != tt.loopback {
				t.Errorf("classifySelfPingAnswer(%q) = (%t, %t), want (%t, %t)", tt.iqType, ok, loopback, tt.wantOK, tt.loopback)
			}
		})
	}
}

func TestIsPingIQGet(t *testing.T) {
	t.Parallel()

	makeIQ := func(iqType stanza.StanzaType, payload stanza.IQPayload) *stanza.IQ {
		iq, err := stanza.NewIQ(stanza.Attrs{Type: iqType, Id: "test-1"})
		if err != nil {
			t.Fatalf("failed to build IQ: %v", err)
		}
		iq.Payload = payload
		return iq
	}

	tests := []struct {
		name string
		iq   *stanza.IQ
		want bool
	}{
		{name: "ping get", iq: makeIQ(stanza.IQTypeGet, &stanza.Ping{}), want: true},
		{name: "ping result", iq: makeIQ(stanza.IQTypeResult, &stanza.Ping{})},
		{name: "ping set", iq: makeIQ(stanza.IQTypeSet, &stanza.Ping{})},
		{name: "disco get", iq: makeIQ(stanza.IQTypeGet, &stanza.DiscoInfo{})},
		{name: "empty get", iq: makeIQ(stanza.IQTypeGet, nil)},
		{name: "nil IQ", iq: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isPingIQGet(tt.iq); got != tt.want {
				t.Errorf("isPingIQGet(%+v) = %t, want %t", tt.iq, got, tt.want)
			}
		})
	}
}

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
