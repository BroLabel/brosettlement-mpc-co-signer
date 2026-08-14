package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifierAcceptsClosedProducerBundles(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	signerID, err := VerifySignerBundle(filepath.Join(root, "contracts", "mpc-2of3", "v1"))
	if err != nil {
		t.Fatalf("VerifySignerBundle() error = %v", err)
	}
	if signerID != "dRgKBw7Y392uHY7AkBj5dkahjKmwYcFYhjtYv62-5mA" {
		t.Fatalf("signer identity = %q", signerID)
	}
	httpID, err := VerifyHTTPBundle(filepath.Join(root, "testdata", "mpc-co-signer-http", "v1"))
	if err != nil {
		t.Fatalf("VerifyHTTPBundle() error = %v", err)
	}
	if httpID != "On6HEeLx2VhbeA6d5070_gopkJdgDkdfwYSlrg1RLFY" {
		t.Fatalf("HTTP identity = %q", httpID)
	}
}

func TestVerifierRejectsChangedMissingAndUnexpectedProducerFiles(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, root string){
		"changed signer vector": func(t *testing.T, root string) {
			write(t, filepath.Join(root, "digest-jcs", "jcs.json"), []byte(`{"a":2}`))
		},
		"missing signer vector": func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "proto.sha256")); err != nil {
				t.Fatal(err)
			}
		},
		"unexpected signer artifact corpus": func(t *testing.T, root string) {
			write(t, filepath.Join(root, "artifact-v1", "primary.json"), []byte(`{}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := copyBundle(t, filepath.Join("..", "..", "..", "contracts", "mpc-2of3", "v1"))
			mutate(t, root)
			if _, err := VerifySignerBundle(root); err == nil {
				t.Fatal("VerifySignerBundle() error = nil")
			}
		})
	}
}

func TestHTTPVerifierRejectsChangedMissingAndUnexpectedProducerFiles(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, root string){
		"changed SIGN claim": func(t *testing.T, root string) {
			write(t, filepath.Join(root, "sign-claim-response.json"), []byte(`{}`))
		},
		"missing SIGN terminal": func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "sign-terminal-completed-request.json")); err != nil {
				t.Fatal(err)
			}
		},
		"unexpected HTTP artifact": func(t *testing.T, root string) {
			write(t, filepath.Join(root, "compatibility-response.json"), []byte(`{}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := copyBundle(t, filepath.Join("..", "..", "..", "testdata", "mpc-co-signer-http", "v1"))
			mutate(t, root)
			if _, err := VerifyHTTPBundle(root); err == nil {
				t.Fatal("VerifyHTTPBundle() error = nil")
			}
		})
	}
}

func TestHTTPFixtureValidatorRejectsBadShapesAndStatuses(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, root string){
		"unknown fixture field": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "mailbox-frame.json", func(value map[string]any) { value["extra"] = true })
		},
		"wrong response status": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "accepted-response.json", func(value map[string]any) { value["httpStatus"] = 201 })
		},
		"mailbox authentication mismatch": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "mailbox-frame.json", func(value map[string]any) { value["authenticatedPartyId"] = "co-signer-recovery" })
		},
		"mailbox numeric string": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "mailbox-frame.json", func(value map[string]any) { value["protocolSeq"] = "1" })
		},
		"null failed result": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "terminal-failed-request.json", func(value map[string]any) { value["terminalResult"].(map[string]any)["result"] = nil })
		},
		"obsolete mailbox message ID": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "mailbox-frame.json", func(value map[string]any) { value["messageId"] = "msg-123" })
		},
		"obsolete DKG session ID": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "listing-response.json", func(value map[string]any) {
				value["pending"].([]any)[0].(map[string]any)["sessionId"] = "dkg-123"
			})
		},
		"unknown own claimed SIGN field": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "listing-response.json", func(value map[string]any) {
				value["ownClaimedSign"].([]any)[0].(map[string]any)["unexpected"] = true
			})
		},
		"removed deployment identity field": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "listing-response.json", func(value map[string]any) {
				value["ownClaimedSign"].([]any)[0].(map[string]any)["coSignerDeploymentId"] = "legacy-installation"
			})
		},
		"missing own claimed SIGN deadline": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "listing-response.json", func(value map[string]any) {
				delete(value["ownClaimedSign"].([]any)[0].(map[string]any), "deadline")
			})
		},
		"wrong own claimed SIGN status": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "listing-response.json", func(value map[string]any) {
				value["ownClaimedSign"].([]any)[0].(map[string]any)["status"] = "PENDING"
			})
		},
		"unknown SIGN claim payload field": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "sign-claim-response.json", func(value map[string]any) {
				value["payload"].(map[string]any)["unexpected"] = true
			})
		},
		"missing SIGN claim digest": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "sign-claim-response.json", func(value map[string]any) {
				delete(value["payload"].(map[string]any), "digest")
			})
		},
		"type-invalid SIGN claim threshold": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "sign-claim-response.json", func(value map[string]any) {
				value["payload"].(map[string]any)["threshold"] = "2"
			})
		},
		"unbound SIGN policy context": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "sign-claim-response.json", func(value map[string]any) {
				value["payload"].(map[string]any)["policyContext"].(map[string]any)["chain"] = "tron:nile"
			})
		},
		"unknown SIGN policy context field": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "sign-claim-response.json", func(value map[string]any) {
				value["payload"].(map[string]any)["policyContext"].(map[string]any)["unexpected"] = true
			})
		},
		"invalid local SIGN timeout status": func(t *testing.T, root string) {
			mutateJSONFixture(t, root, "sign-terminal-timed-out-local.json", func(value map[string]any) {
				value["status"] = "FAILED"
			})
		},
		"SIGN terminal uses DKG family": func(t *testing.T, root string) {
			raw, err := os.ReadFile(filepath.Join(root, "terminal-completed-request.json"))
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(root, "sign-terminal-completed-request.json"), raw)
		},
		"DKG terminal uses SIGN family": func(t *testing.T, root string) {
			raw, err := os.ReadFile(filepath.Join(root, "sign-terminal-completed-request.json"))
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(root, "terminal-completed-request.json"), raw)
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := copyBundle(t, filepath.Join("..", "..", "..", "testdata", "mpc-co-signer-http", "v1"))
			mutate(t, root)
			if err := verifyHTTPFixtures(root); err == nil {
				t.Fatal("verifyHTTPFixtures() error = nil")
			}
		})
	}
}

func mutateJSONFixture(t *testing.T, root, name string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(root, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	mutate(value)
	raw, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, raw)
}

func copyBundle(t *testing.T, source string) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "v1")
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	return destination
}

func write(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}
