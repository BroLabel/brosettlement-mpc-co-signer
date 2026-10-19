package approval

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/signingpolicy"
)

func TestTrustedTronNetworkEvidence(t *testing.T) {
	for _, test := range []string{"valid", "mainnet", "wrong-chain", "wrong-reference", "malformed-height", "wrong-id", "rpc-error", "redirect", "timeout", "unconfigured", "oversized", "duplicate-json", "bad-block-prefix"} {
		t.Run(test, func(t *testing.T) {
			var id [32]byte
			binary.BigEndian.PutUint64(id[:8], 0x11234)
			copy(id[8:16], []byte{1, 2, 3, 4, 5, 6, 7, 8})
			chain := "tron:nile"
			network := "0xcd8690dc"
			if test == "mainnet" {
				chain = "tron:mainnet"
				network = "0x2b6653dc"
			}
			var requests []string
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var v struct {
					Method string            `json:"method"`
					Params []json.RawMessage `json:"params"`
				}
				_ = json.NewDecoder(r.Body).Decode(&v)
				requests = append(requests, v.Method)
				if test == "redirect" {
					http.Redirect(w, r, "https://forbidden.example", http.StatusFound)
					return
				}
				if test == "timeout" {
					select {
					case <-r.Context().Done():
					case <-time.After(100 * time.Millisecond):
					}
					return
				}
				if test == "oversized" {
					_, _ = w.Write(make([]byte, 8*1024*1024+1))
					return
				}
				if test == "wrong-id" {
					fmt.Fprint(w, `{"jsonrpc":"2.0","id":2,"result":"0xcd8690dc"}`)
					return
				}
				if test == "rpc-error" {
					fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-1}}`)
					return
				}
				if test == "duplicate-json" {
					fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"id":1,"result":"0xcd8690dc"}`)
					return
				}
				if v.Method == "eth_chainId" {
					if test == "wrong-chain" {
						network = "0x99"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": network})
					return
				}
				var height string
				_ = json.Unmarshal(v.Params[0], &height)
				n := uint64(0x11235)
				if height != "latest" {
					if height != "0x11234" {
						t.Errorf("reference height=%s", height)
					}
					n = 0x11234
				}
				blockID := id
				binary.BigEndian.PutUint64(blockID[:8], n)
				if test == "wrong-reference" && height != "latest" {
					blockID[8] = 99
				}
				if test == "bad-block-prefix" {
					blockID[0] = 99
				}
				number := fmt.Sprintf("0x%x", n)
				if test == "malformed-height" {
					number = "0x01"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"number": number, "hash": "0x" + hex.EncodeToString(blockID[:]), "transactions": []string{"0xlegitimate-standard-field"}}})
			}))
			defer srv.Close()
			endpoints := map[string]string{chain: srv.URL}
			if test == "unconfigured" {
				endpoints = nil
			}
			timeout := time.Second
			if test == "timeout" {
				timeout = 10 * time.Millisecond
			}
			checker, e := NewTronNetworkChecker(endpoints, timeout, srv.Client().Transport)
			if e != nil {
				t.Fatal(e)
			}
			e = checker.Verify(context.Background(), chain, &signingpolicy.Tapos{RefBytes: []byte{0x12, 0x34}, RefHash: id[8:16]})
			if test == "valid" || test == "mainnet" {
				if e != nil || len(requests) != 3 {
					t.Fatalf("requests=%v err=%v", requests, e)
				}
			} else if e == nil {
				t.Fatal("hostile evidence accepted")
			}
			if test == "timeout" && !errors.Is(e, ErrNetworkUnavailable) {
				t.Fatal("outage misclassified")
			}
			if (test == "wrong-reference" || test == "wrong-chain") && !errors.Is(e, ErrNetworkMismatch) {
				t.Fatal("mismatch misclassified")
			}
		})
	}
}
func TestTronNetworkConfiguration(t *testing.T) {
	for _, endpoint := range []string{"http://example.test", "https://user:secret@example.test", "https://example.test/#fragment", "https:///missing"} {
		if _, e := NewTronNetworkChecker(map[string]string{"tron:nile": endpoint}, time.Second, nil); e == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
}
func TestEmptyTronCheckerPreservesLegacyZeroTimeout(t *testing.T) {
	if _, e := NewTronNetworkChecker(nil, 0, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := NewTronNetworkChecker(map[string]string{"tron:nile": "https://synthetic.example"}, 0, nil); e == nil {
		t.Fatal("configured checker without bounded timeout")
	}
}
