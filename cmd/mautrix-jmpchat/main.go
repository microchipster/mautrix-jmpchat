package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"jmpchat/connector"
)

var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	connector := &connector.MyConnector{}

	m := mxmain.BridgeMain{
		Name:        "jmpchat",
		Description: "A Matrix-XMPP bridge for JMP.chat/Cheogram",
		Version:     "0.1.0",
		URL:         "",
		Connector:   connector,
	}

	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
