// SPDX-FileCopyrightText: 2026 Slavi Pantaleev
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package connector

import "testing"

func TestMyNetworkClientTryStartConnect(t *testing.T) {
	client := &MyNetworkClient{}

	attempt, started := client.tryStartConnect()
	if !started || attempt != 1 {
		t.Fatalf("first connection launch = (attempt %d, started %t), want (1, true)", attempt, started)
	}

	attempt, started = client.tryStartConnect()
	if started || attempt != 1 {
		t.Fatalf("overlapping connection launch = (attempt %d, started %t), want (1, false)", attempt, started)
	}

	client.releaseConnect()
	attempt, started = client.tryStartConnect()
	if !started || attempt != 1 {
		t.Fatalf("connection launch after release = (attempt %d, started %t), want (1, true)", attempt, started)
	}
}
