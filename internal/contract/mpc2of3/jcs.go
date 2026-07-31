package mpc2of3

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"

	"github.com/gowebpki/jcs"
)

var unsignedJSONInteger = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

const maxSafeJSONInteger = uint64(1<<53 - 1)

func canonicalJCS(raw []byte) ([]byte, error) {
	if err := validateJSONTokens(raw); err != nil {
		return nil, err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize JCS: %w", err)
	}
	return canonical, nil
}

func requireCanonicalJCS(raw []byte) error {
	canonical, err := canonicalJCS(raw)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return fmt.Errorf("JSON is not exact JCS bytes")
	}
	return nil
}

// RequireCanonicalJCS is the local RFC 8785 adapter shared by closed bundles.
func RequireCanonicalJCS(raw []byte) error { return requireCanonicalJCS(raw) }

func validateJSONTokens(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("JSON has trailing data")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				if err := scanJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := scanJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		}
	case json.Number:
		if !unsignedJSONInteger.MatchString(string(value)) {
			return fmt.Errorf("JSON number %q is not a canonical bounded integer", value)
		}
		number, err := strconv.ParseUint(string(value), 10, 64)
		if err != nil || number > maxSafeJSONInteger {
			return fmt.Errorf("JSON number %q is not a canonical bounded integer", value)
		}
	}
	return nil
}

func decodeClosed(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("JSON has trailing data")
	}
	return nil
}

func ascii(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func printableASCII(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	for _, b := range raw {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}
