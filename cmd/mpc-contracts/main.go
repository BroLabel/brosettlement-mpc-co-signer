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
	preparedRoot := flags.String("prepared-root", "testdata/approval-prepared-v1", "prepared SIGN fixture bundle root")
	approvalRoot := flags.String("approval-root", "testdata/approval-v1", "approval fixture bundle root")
	_ = flags.Parse(args)
	signerID, err := bundle.VerifySignerBundle(*signerRoot)
	if err != nil {
		fail("verify signer bundle: %v", err)
	}
	httpID, err := bundle.VerifyHTTPBundle(*httpRoot)
	if err != nil {
		fail("verify HTTP bundle: %v", err)
	}
	approvalID, err := bundle.VerifyApprovalBundle(*approvalRoot)
	if err != nil {
		fail("verify approval bundle: %v", err)
	}
	preparedID, err := bundle.VerifyPreparedBundle(*preparedRoot)
	if err != nil {
		fail("verify prepared SIGN bundle: %v", err)
	}
	fmt.Printf("PREPARED-SIGN-V2 verified: %s\n", preparedID)
	fmt.Printf("APPROVAL-V1 verified: %s\n", approvalID)
	fmt.Printf("CONTRACT-BUNDLE-V1 verified: %s\nCOSIGNER-HTTP-V1 verified: %s\n", signerID, httpID)
}

func fail(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }
