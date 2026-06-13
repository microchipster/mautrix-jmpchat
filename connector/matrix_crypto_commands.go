package connector

import (
	"strings"

	"maunium.net/go/mautrix/bridgev2/commands"
	matrixbridge "maunium.net/go/mautrix/bridgev2/matrix"
)

func getMatrixConnector(ce *commands.Event) (*matrixbridge.Connector, bool) {
	if ce == nil || ce.Bridge == nil {
		return nil, false
	}
	matrix, ok := ce.Bridge.Matrix.(*matrixbridge.Connector)
	if !ok || matrix == nil {
		return nil, false
	}
	return matrix, true
}

func matrixEncryptionResetRequested(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "yes", "--yes", "-y", "force", "--force":
		return true
	default:
		return false
	}
}

var CommandMatrixEncryptionResync = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		matrix, ok := getMatrixConnector(ce)
		if !ok {
			ce.Reply("Matrix bridge connector is not available")
			return
		}
		if matrix.Crypto == nil {
			ce.Reply("Matrix encryption is not enabled in this bridge instance. These commands only help with Matrix E2EE decrypt/session issues.")
			return
		}
		matrix.ResyncEncryptionState(ce.Ctx)
		ce.Reply("Resynced Matrix encryption state. Use this first for decrypt/key errors; if messages still fail, run reset-matrix-encryption yes. Existing rooms and history were kept.")
	},
	Name:    "resync-matrix-encryption",
	Aliases: []string{"resync-encryption", "crypto-resync"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Retry Matrix encryption after a device/session mismatch or decrypt failure, without deleting rooms or history",
	},
	RequiresLogin: true,
}

var CommandMatrixEncryptionReset = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		matrix, ok := getMatrixConnector(ce)
		if !ok {
			ce.Reply("Matrix bridge connector is not available")
			return
		}
		if matrix.Crypto == nil {
			ce.Reply("Matrix encryption is not enabled in this bridge instance. These commands only help with Matrix E2EE decrypt/session issues.")
			return
		}
		if !matrixEncryptionResetRequested(ce.Args) {
			ce.Reply("Usage: reset-matrix-encryption yes")
			return
		}
		if err := matrix.Crypto.Reset(ce.Ctx, true); err != nil {
			ce.Reply("Failed to reset Matrix encryption: %v", err)
			return
		}
		matrix.ResyncEncryptionState(ce.Ctx)
		ce.Reply("Reset Matrix crypto and resynced encryption state. Use this when resync was not enough, such as stale device/signing-key problems. Existing rooms and history were kept.")
	},
	Name:    "reset-matrix-encryption",
	Aliases: []string{"reset-encryption", "crypto-reset"},
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Rebuild Matrix crypto state after resync is not enough, for stale device/session or signing-key problems; keeps rooms and history",
		Args:        "yes",
	},
	RequiresLogin: true,
}
