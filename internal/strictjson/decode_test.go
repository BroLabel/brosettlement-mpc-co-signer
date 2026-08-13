package strictjson

import "testing"

type fixture struct {
	Name string `json:"name"`
}

func TestDecodeClosedRejectsAmbiguousOrOpenJSON(t *testing.T) {
	for _, raw := range []string{
		`{"name":"first","name":"second"}`,
		`{"name":"value","unknown":true}`,
		`{"name":"value"}{"name":"second"}`,
		`{"name":{"nested":true,"nested":false}}`,
	} {
		var target fixture
		if err := DecodeClosed([]byte(raw), &target); err == nil {
			t.Fatalf("DecodeClosed(%s) error = nil", raw)
		}
	}

	var target fixture
	if err := DecodeClosed([]byte(`{"name":"value"}`), &target); err != nil {
		t.Fatalf("DecodeClosed() error = %v", err)
	}
	if target.Name != "value" {
		t.Fatalf("DecodeClosed() name = %q", target.Name)
	}
}

func TestDecodeCanonicalRequiresExactCompactFieldRepresentation(t *testing.T) {
	var target fixture
	if err := DecodeCanonical([]byte(`{"name":"value"}`), &target); err != nil {
		t.Fatalf("DecodeCanonical(canonical) error = %v", err)
	}
	if err := DecodeCanonical([]byte("{\n  \"name\": \"value\"\n}"), &target); err == nil {
		t.Fatal("DecodeCanonical(noncanonical) error = nil")
	}
}
