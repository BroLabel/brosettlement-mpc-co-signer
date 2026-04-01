package main

import (
	"fmt"

	"github.com/BroLabel/brosettlement-mpc-core/protocol"
)

func main() {
	frame := protocol.Frame{
		SessionID: "bootstrap-session",
		FromParty: "co-signer",
		Payload:   []byte("bootstrap"),
	}

	fmt.Printf("brosettlement-mpc-co-signer bootstrap: session=%s from=%s payload=%d\n", frame.SessionID, frame.FromParty, len(frame.Payload))
}
