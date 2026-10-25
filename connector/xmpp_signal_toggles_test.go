// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSimpleConfigSignalToggles(t *testing.T) {
	t.Parallel()

	var defaults SimpleConfig
	if !defaults.sendTypingEnabled() {
		t.Error("sendTypingEnabled() default = false, want true")
	}
	if !defaults.sendReadMarkersEnabled() {
		t.Error("sendReadMarkersEnabled() default = false, want true")
	}
	if !defaults.sendDeliveryReceiptsEnabled() {
		t.Error("sendDeliveryReceiptsEnabled() default = false, want true")
	}

	var nilConfig *SimpleConfig
	if !nilConfig.sendTypingEnabled() || !nilConfig.sendReadMarkersEnabled() || !nilConfig.sendDeliveryReceiptsEnabled() {
		t.Error("nil config should default to enabled signals")
	}

	raw := "send_typing: false\nsend_read_markers: false\nsend_delivery_receipts: false\n"
	var disabled SimpleConfig
	if err := yaml.Unmarshal([]byte(raw), &disabled); err != nil {
		t.Fatalf("yaml.Unmarshal() error: %v", err)
	}
	if disabled.sendTypingEnabled() {
		t.Error("sendTypingEnabled() = true, want false")
	}
	if disabled.sendReadMarkersEnabled() {
		t.Error("sendReadMarkersEnabled() = true, want false")
	}
	if disabled.sendDeliveryReceiptsEnabled() {
		t.Error("sendDeliveryReceiptsEnabled() = true, want false")
	}
}
