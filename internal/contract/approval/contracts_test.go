package approval

import (
	"encoding/base64"
	"encoding/json"
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
	var corpus struct {
		PublicKeyBase64    string   `json:"publicKeyBase64"`
		CursorSecretBase64 string   `json:"cursorSecretBase64"`
		Vectors            []Vector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	public, _ := base64.StdEncoding.DecodeString(corpus.PublicKeyBase64)
	secret, _ := base64.StdEncoding.DecodeString(corpus.CursorSecretBase64)
	for _, vector := range corpus.Vectors {
		t.Run(vector.ID, func(t *testing.T) {
			err := VerifyVector(vector, public, secret)
			if (err == nil) != (vector.Expected == "ACCEPT") {
				t.Fatalf("expected %s, got %v", vector.Expected, err)
			}
		})
	}
}
