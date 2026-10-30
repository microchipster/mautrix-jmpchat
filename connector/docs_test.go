// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"
)

func TestTopicGuidesAndKeywords(t *testing.T) {
	names := topicNames()
	if len(names) < 5 {
		t.Fatalf("topicNames = %v, want at least 5 topics", names)
	}
	for _, name := range names {
		guide, ok := topicGuide(name)
		if !ok || strings.TrimSpace(guide) == "" {
			t.Errorf("topicGuide(%q) = %q, %v", name, guide, ok)
		}
	}
	for keyword, topic := range keywordTopics {
		if _, ok := topicGuide(topic); !ok {
			t.Errorf("keyword %q maps to unknown topic %q", keyword, topic)
		}
	}
}

func TestClosestCommand(t *testing.T) {
	known := map[string]struct{}{
		"help": {}, "sip-login": {}, "sip-status": {}, "sync": {}, "call": {},
	}
	cases := []struct {
		token string
		want  string
		ok    bool
	}{
		{"helo", "help", true},
		{"sip-logn", "sip-login", true},
		{"sip-stauts", "sip-status", true},
		{"zzzz", "", false},
		{"ab", "", false},
	}
	for _, tc := range cases {
		got, ok := closestCommand(tc.token, known)
		if got != tc.want || ok != tc.ok {
			t.Errorf("closestCommand(%q) = %q, %v; want %q, %v", tc.token, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCommandCatalog(t *testing.T) {
	proc, ok := commands.NewProcessor(&bridgev2.Bridge{}).(*commands.Processor)
	if !ok {
		t.Fatal("NewProcessor did not return a *commands.Processor")
	}
	known := commandCatalog(proc)
	for _, want := range []string{"help", "login", "cancel", "sudo", "create", "start-chat"} {
		if _, ok := known[want]; !ok {
			t.Errorf("catalog is missing %q", want)
		}
	}
	if len(known) < 30 {
		t.Errorf("catalog has %d entries, want the built-ins", len(known))
	}
}

func TestShouldIntercept(t *testing.T) {
	p := &intelligentProcessor{br: &bridgev2.Bridge{}, known: map[string]struct{}{"help": {}, "call": {}}}
	user := &bridgev2.User{User: &database.User{ManagementRoom: "!mgmt:example.com"}}
	cases := []struct {
		name    string
		user    *bridgev2.User
		roomID  id.RoomID
		message string
		want    bool
	}{
		{"topic keyword", user, user.ManagementRoom, "sip please", true},
		{"question with keyword", user, user.ManagementRoom, "how do I set up calls?", true},
		{"known command", user, user.ManagementRoom, "help", false},
		{"known command with args", user, user.ManagementRoom, "call +15551234567", false},
		{"empty message", user, user.ManagementRoom, "", true},
		{"other room", user, "!other:example.com", "sip please", false},
		{"no user", nil, user.ManagementRoom, "sip please", false},
	}
	for _, tc := range cases {
		if got := p.shouldIntercept(tc.user, tc.roomID, tc.message); got != tc.want {
			t.Errorf("%s: shouldIntercept = %v, want %v", tc.name, got, tc.want)
		}
	}
	empty := &intelligentProcessor{br: &bridgev2.Bridge{}, known: map[string]struct{}{}}
	if empty.shouldIntercept(user, user.ManagementRoom, "sip please") {
		t.Error("an empty catalog must delegate everything")
	}
}

func TestResponseRouting(t *testing.T) {
	p := &intelligentProcessor{br: &bridgev2.Bridge{}, known: map[string]struct{}{"help": {}, "sip-login": {}, "sip-status": {}}}
	if reply := p.response("how do I set up calls?"); !strings.Contains(reply, "Calling") {
		t.Errorf("calls question got %q", reply)
	}
	if reply := p.response("billing?"); !strings.Contains(reply, "JMP account chat") {
		t.Errorf("billing got %q", reply)
	}
	if reply := p.response("everything is broken"); !strings.Contains(reply, "Troubleshooting") {
		t.Errorf("broken got %q", reply)
	}
	if reply := p.response("zzzz"); !strings.Contains(reply, "help") {
		t.Errorf("nonsense got %q", reply)
	}
	if reply := p.response("sip-logn"); !strings.Contains(reply, "sip-login") {
		t.Errorf("typo got %q", reply)
	}
}
