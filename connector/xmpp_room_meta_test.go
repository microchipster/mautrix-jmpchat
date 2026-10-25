// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

func TestMucNickSyncEnabledDefaults(t *testing.T) {
	t.Parallel()
	var nilConfig *SimpleConfig
	if !nilConfig.mucNickSyncEnabled() {
		t.Error("nil config should enable nick sync")
	}
	if !(&SimpleConfig{}).mucNickSyncEnabled() {
		t.Error("nick sync should be enabled by default")
	}
	disabled := false
	if (&SimpleConfig{MUCNickSync: &disabled}).mucNickSyncEnabled() {
		t.Error("explicit false should disable nick sync")
	}
	enabled := true
	if !(&SimpleConfig{MUCNickSync: &enabled}).mucNickSyncEnabled() {
		t.Error("explicit true should enable nick sync")
	}
}

func TestMucNickFromRoomName(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		nick string
		ok   bool
	}{
		"nick":        {"nick", true},
		"O'Brien":     {"O'Brien", true},
		"  nick  ":    {"nick", true},
		"Family Room": {"", false},
		"":            {"", false},
		"   ":         {"", false},
		"nick\tx":     {"", false},
	}
	for input, want := range cases {
		nick, ok := mucNickFromRoomName(input)
		if nick != want.nick || ok != want.ok {
			t.Errorf("mucNickFromRoomName(%q) = (%q, %v), want (%q, %v)", input, nick, ok, want.nick, want.ok)
		}
	}
}

func testPortal() *bridgev2.Portal {
	return &bridgev2.Portal{Portal: &database.Portal{ID: networkid.PortalID("+15550101100@cheogram.com")}}
}

func TestHandleMatrixRoomNameGuards(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	if changed, err := nc.HandleMatrixRoomName(t.Context(), nil); changed || err != nil {
		t.Errorf("nil message should be ignored, got (%v, %v)", changed, err)
	}
	if changed, err := nc.HandleMatrixRoomName(t.Context(), &bridgev2.MatrixRoomName{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomNameEventContent]{Portal: testPortal()},
	}); changed || err != nil {
		t.Errorf("nil content should be ignored, got (%v, %v)", changed, err)
	}
	if changed, err := nc.HandleMatrixRoomName(t.Context(), &bridgev2.MatrixRoomName{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomNameEventContent]{
			Portal:  testPortal(),
			Content: &event.RoomNameEventContent{Name: "   "},
		},
	}); changed || err != nil {
		t.Errorf("empty name should be ignored, got (%v, %v)", changed, err)
	}
}

func TestHandleMatrixRoomNameRecordsNameWhileDisconnected(t *testing.T) {
	t.Parallel()
	portal := testPortal()
	nc := &MyNetworkClient{}
	changed, err := nc.HandleMatrixRoomName(t.Context(), &bridgev2.MatrixRoomName{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomNameEventContent]{
			Portal:  portal,
			Content: &event.RoomNameEventContent{Name: "My Friend"},
		},
	})
	if !changed || err != nil {
		t.Fatalf("rename should be recorded, got (%v, %v)", changed, err)
	}
	if portal.Name != "My Friend" || !portal.NameSet || !portal.NameIsCustom {
		t.Errorf("unexpected portal name state: %q set=%v custom=%v", portal.Name, portal.NameSet, portal.NameIsCustom)
	}
}

func TestHandleMatrixRoomTopicGuards(t *testing.T) {
	t.Parallel()
	nc := &MyNetworkClient{}
	if changed, err := nc.HandleMatrixRoomTopic(t.Context(), nil); changed || err != nil {
		t.Errorf("nil message should be ignored, got (%v, %v)", changed, err)
	}
	if changed, err := nc.HandleMatrixRoomTopic(t.Context(), &bridgev2.MatrixRoomTopic{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.TopicEventContent]{Portal: testPortal()},
	}); changed || err != nil {
		t.Errorf("nil content should be ignored, got (%v, %v)", changed, err)
	}
}

func TestHandleMatrixRoomTopicRecordsTopicWhileDisconnected(t *testing.T) {
	t.Parallel()
	portal := testPortal()
	nc := &MyNetworkClient{}
	changed, err := nc.HandleMatrixRoomTopic(t.Context(), &bridgev2.MatrixRoomTopic{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.TopicEventContent]{
			Portal:  portal,
			Content: &event.TopicEventContent{Topic: " Weekend plans "},
		},
	})
	if !changed || err != nil {
		t.Fatalf("topic change should be recorded, got (%v, %v)", changed, err)
	}
	if portal.Topic != "Weekend plans" || !portal.TopicSet {
		t.Errorf("unexpected portal topic state: %q set=%v", portal.Topic, portal.TopicSet)
	}
}
