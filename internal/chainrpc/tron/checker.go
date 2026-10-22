// Package tron verifies TRON network membership through trusted JSON-RPC endpoints.
package tron

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/signingpolicy"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/strictjson"
	core "github.com/fbsobreira/gotron-sdk/pkg/proto/core"
	"google.golang.org/protobuf/proto"
)

var ErrNetworkUnavailable = signingpolicy.ErrNetworkUnavailable
var ErrNetworkMismatch = signingpolicy.ErrNetworkMismatch

// Checker trusts only operator-configured exact HTTPS endpoints.
// URLs and transport errors are never returned, persisted or logged.
type Checker struct {
	endpoints map[string]string
	client    *http.Client
	timeout   time.Duration
}

func NewChecker(endpoints map[string]string, timeout time.Duration, transport http.RoundTripper) (*Checker, error) {
	if len(endpoints) > 0 && timeout <= 0 {
		return nil, ErrNetworkUnavailable
	}
	copyEndpoints := make(map[string]string)
	for chain, endpoint := range endpoints {
		u, e := url.Parse(endpoint)
		if chain != "tron:mainnet" && chain != "tron:nile" || e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return nil, ErrNetworkUnavailable
		}
		copyEndpoints[chain] = endpoint
	}
	return &Checker{copyEndpoints, &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, timeout}, nil
}
func (c *Checker) call(ctx context.Context, endpoint, method string, params any) (json.RawMessage, error) {
	body, _ := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", 1, method, params})
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if e != nil {
		return nil, ErrNetworkUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.client.Do(req)
	if e != nil {
		return nil, ErrNetworkUnavailable
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if e != nil || len(raw) > 8*1024*1024 || resp.StatusCode != 200 {
		return nil, ErrNetworkUnavailable
	}
	var v struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if strictjson.DecodeClosed(raw, &v) != nil || v.JSONRPC != "2.0" || v.ID != 1 || len(v.Result) == 0 || string(v.Result) == "null" || len(v.Error) > 0 && string(v.Error) != "null" {
		return nil, ErrNetworkUnavailable
	}
	return v.Result, nil
}
func quantity(raw string) (uint64, error) {
	if !strings.HasPrefix(raw, "0x") || len(raw) < 3 || len(raw) > 18 || len(raw) > 3 && raw[2] == '0' {
		return 0, ErrNetworkUnavailable
	}
	n, e := strconv.ParseUint(raw[2:], 16, 64)
	if e != nil || "0x"+strconv.FormatUint(n, 16) != raw {
		return 0, ErrNetworkUnavailable
	}
	return n, nil
}
func block(raw []byte) (uint64, []byte, error) {
	// Normal standard block fields are intentionally accepted. Duplicate keys,
	// malformed required fields and invalid blockID height prefixes are rejected.
	var v map[string]json.RawMessage
	if strictjson.DecodeClosed(raw, &v) != nil {
		return 0, nil, ErrNetworkUnavailable
	}
	var height, hash string
	if json.Unmarshal(v["number"], &height) != nil || json.Unmarshal(v["hash"], &hash) != nil {
		return 0, nil, ErrNetworkUnavailable
	}
	n, e := quantity(height)
	if e != nil {
		return 0, nil, e
	}
	id, e := hex.DecodeString(strings.TrimPrefix(hash, "0x"))
	if e != nil || len(id) != 32 || "0x"+hex.EncodeToString(id) != hash || binary.BigEndian.Uint64(id[:8]) != n {
		return 0, nil, ErrNetworkUnavailable
	}
	return n, id, nil
}
func (c *Checker) Verify(ctx context.Context, chain string, rawTransaction []byte) error {
	var t core.TransactionRaw
	if c == nil || proto.Unmarshal(rawTransaction, &t) != nil || len(t.RefBlockBytes) != 2 || len(t.RefBlockHash) != 8 {
		return ErrNetworkUnavailable
	}
	endpoint := c.endpoints[chain]
	if endpoint == "" {
		return ErrNetworkUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	raw, e := c.call(ctx, endpoint, "eth_chainId", []any{})
	if e != nil {
		return e
	}
	expected := map[string]string{"tron:mainnet": "0x2b6653dc", "tron:nile": "0xcd8690dc"}[chain]
	var id string
	if json.Unmarshal(raw, &id) != nil {
		return ErrNetworkUnavailable
	}
	if id != expected {
		return ErrNetworkMismatch
	}
	raw, e = c.call(ctx, endpoint, "eth_getBlockByNumber", []any{"latest", false})
	if e != nil {
		return e
	}
	head, _, e := block(raw)
	if e != nil {
		return e
	}
	ref := (head & ^uint64(65535)) | uint64(binary.BigEndian.Uint16(t.RefBlockBytes))
	if ref > head {
		if ref < 65536 {
			return ErrNetworkMismatch
		}
		ref -= 65536
	}
	raw, e = c.call(ctx, endpoint, "eth_getBlockByNumber", []any{"0x" + strconv.FormatUint(ref, 16), false})
	if e != nil {
		return e
	}
	height, hash, e := block(raw)
	if e != nil {
		return e
	}
	if height != ref || !bytes.Equal(hash[6:8], t.RefBlockBytes) || !bytes.Equal(hash[8:16], t.RefBlockHash) {
		return ErrNetworkMismatch
	}
	return nil
}
