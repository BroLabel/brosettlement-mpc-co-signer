package approval

import (
	"encoding/base64"
	"encoding/json"
	"github.com/gowebpki/jcs"
	"os"
	"testing"
)

func TestBackendOwnedCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/approval-v1/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCorpus(raw); err != nil {
		t.Fatal(err)
	}
}

func TestPendingPageRejectsUnboundedDuplicateUnorderedAndNonASCIICursor(t *testing.T) {
	raw, _ := os.ReadFile("../../../testdata/approval-v1/vectors.json")
	var corpus struct {
		Vectors []Vector `json:"vectors"`
	}
	json.Unmarshal(raw, &corpus)
	auth, _ := base64.StdEncoding.DecodeString(corpus.Vectors[0].InputBase64)
	var a map[string]any
	json.Unmarshal(auth, &a)
	org := a["orgId"].(string)
	req := a["requestId"].(string)
	item := map[string]any{"requestId": req, "createdAt": "2026-10-19T01:00:00.000Z", "authorization": json.RawMessage(auth), "authorizationHash": hash(auth)}
	a["requestId"] = req + "-later"
	otherRaw, _ := json.Marshal(a)
	otherRaw, _ = jcs.Transform(otherRaw)
	earlier := map[string]any{"requestId": req + "-later", "createdAt": "2026-10-19T00:00:00.000Z", "authorization": json.RawMessage(otherRaw), "authorizationHash": hash(otherRaw)}
	sameTimeEarlierID := map[string]any{"requestId": req + "-later", "createdAt": "2026-10-19T01:00:00.000Z", "authorization": json.RawMessage(otherRaw), "authorizationHash": hash(otherRaw)}

	for name, page := range map[string]map[string]any{
		"descending timestamp": {"version": 1, "items": []any{item, earlier}, "nextCursor": nil},
		"descending tie ID":    {"version": 1, "items": []any{sameTimeEarlierID, item}, "nextCursor": nil},
		"duplicate":            {"version": 1, "items": []any{item, item}, "nextCursor": nil},
		"non ASCII cursor":     {"version": 1, "items": []any{}, "nextCursor": "токен"},
		"too many": {"version": 1, "items": func() []any {
			v := []any{}
			for i := 0; i < 101; i++ {
				v = append(v, item)
			}
			return v
		}(), "nextCursor": nil},
	} {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(page)
			if ParsePendingPage(b, org) == nil {
				t.Fatal("unsafe page accepted")
			}
		})
	}
}

func TestPendingPageRejectsCursorSpaces(t *testing.T) {
	for _, tc := range []struct{ name, cursor string }{
		{"leading space", " cursor"},
		{"interior space", "cur sor"},
		{"trailing space", "cursor "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"version": 1, "items": []any{}, "nextCursor": tc.cursor})
			if err != nil {
				t.Fatal(err)
			}
			if ParsePendingPage(raw, "org-1") == nil {
				t.Fatal("cursor containing a literal space accepted")
			}
		})
	}
}
