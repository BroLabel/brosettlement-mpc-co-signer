package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/bundle"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: mpc-contracts verify")
	}
	switch os.Args[1] {
	case "verify":
		verify(os.Args[2:])
	default:
		fail("unknown command %q", os.Args[1])
	}
}

func verify(args []string) {
	flags := flag.NewFlagSet("verify", flag.ExitOnError)
	signerRoot := flags.String("signer-root", "contracts/mpc-2of3/v1", "signer contract bundle root")
	httpRoot := flags.String("http-root", "testdata/mpc-co-signer-http/v1", "HTTP fixture bundle root")
	_ = flags.Parse(args)
	signerID, err := bundle.VerifySignerBundle(*signerRoot)
	if err != nil {
		fail("verify signer bundle: %v", err)
	}
	httpID, err := bundle.VerifyHTTPBundle(*httpRoot)
	if err != nil {
		fail("verify HTTP bundle: %v", err)
	}
	fmt.Printf("CONTRACT-BUNDLE-V1 verified: %s\nCOSIGNER-HTTP-V1 verified: %s\n", signerID, httpID)
}

func fail(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }
