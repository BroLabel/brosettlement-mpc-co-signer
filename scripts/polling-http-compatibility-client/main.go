// Command polling-http-compatibility-client exercises the real Go discovery client
// against the isolated backend HTTP regression fixture. It never starts MPC work.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
)

func main() {
	seed, err := base64.StdEncoding.DecodeString(os.Getenv("POLLING_TEST_SEED"))
	if err != nil || len(seed) != ed25519.SeedSize {
		panic("POLLING_TEST_SEED must be a base64 Ed25519 seed")
	}
	client := monolith.New(os.Getenv("POLLING_TEST_URL"), os.Getenv("POLLING_TEST_KEY_ID"), ed25519.NewKeyFromSeed(seed), 5*time.Second, monolith.BuildIdentity{Version: os.Getenv("POLLING_TEST_VERSION"), Revision: os.Getenv("POLLING_TEST_REVISION")})
	result, err := client.ListActionableIntents(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]int{"ownClaimedDkg": len(result.OwnClaimedDKG), "ownClaimedSign": len(result.OwnClaimedSign), "pending": len(result.Pending)}); err != nil {
		panic(err)
	}
}
