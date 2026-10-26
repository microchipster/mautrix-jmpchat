// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"testing"
	"time"
)

func TestMUCInviteLimiterAllow(t *testing.T) {
	t.Parallel()
	limiter := &mucInviteLimiter{}
	now := time.Now()
	for i := 0; i < mucInviteRateLimit; i++ {
		if !limiter.allow(now) {
			t.Fatalf("invite %d should be allowed", i+1)
		}
	}
	if limiter.allow(now) {
		t.Fatal("invite beyond the limit should be denied")
	}
	if !limiter.allow(now.Add(mucInviteRateWindow + time.Minute)) {
		t.Fatal("invite after the window should be allowed again")
	}
}

func TestMUCInviteLimiterWindowPruning(t *testing.T) {
	t.Parallel()
	limiter := &mucInviteLimiter{}
	start := time.Now()
	for i := 0; i < mucInviteRateLimit; i++ {
		if !limiter.allow(start.Add(time.Duration(i) * time.Minute)) {
			t.Fatalf("invite %d should be allowed", i+1)
		}
	}
	// The oldest invite has expired by the time the next one arrives, so it
	// fits within the window again.
	if !limiter.allow(start.Add(mucInviteRateWindow + 30*time.Second)) {
		t.Fatal("invite after partial expiry should be allowed")
	}
}

func TestMUCInviteLimiterIndependentInstances(t *testing.T) {
	t.Parallel()
	first := &mucInviteLimiter{}
	second := &mucInviteLimiter{}
	now := time.Now()
	for i := 0; i < mucInviteRateLimit; i++ {
		first.allow(now)
	}
	if first.allow(now) {
		t.Fatal("first limiter should be exhausted")
	}
	if !second.allow(now) {
		t.Fatal("second limiter should be unaffected")
	}
}
