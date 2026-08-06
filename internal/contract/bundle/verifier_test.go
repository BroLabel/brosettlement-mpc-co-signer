package bundle

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	if httpID != "DfiDHjeT5xYiTN7ICY9XDAjHP9XioZYO4N2EAYaH5WQ" {
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

func TestSyncRejectsInvalidSourceWithoutChangingEitherDestination(t *testing.T) {
	root := t.TempDir()
	signerSource := copyBundle(t, filepath.Join("..", "..", "..", "contracts", "mpc-2of3", "v1"))
	httpSource := copyBundle(t, filepath.Join("..", "..", "..", "testdata", "mpc-co-signer-http", "v1"))
	if err := Sync(root, signerSource, httpSource); err != nil {
		t.Fatalf("initial Sync() error = %v", err)
	}
	beforeSigner := bundleBytes(t, filepath.Join(root, "contracts", "mpc-2of3", "v1"))
	beforeHTTP := bundleBytes(t, filepath.Join(root, "testdata", "mpc-co-signer-http", "v1"))
	write(t, filepath.Join(signerSource, "unexpected.json"), []byte(`{}`))
	if err := Sync(root, signerSource, httpSource); err == nil {
		t.Fatal("Sync() error = nil")
	}
	if got := bundleBytes(t, filepath.Join(root, "contracts", "mpc-2of3", "v1")); string(got) != string(beforeSigner) {
		t.Fatal("invalid signer source changed signer destination")
	}
	if got := bundleBytes(t, filepath.Join(root, "testdata", "mpc-co-signer-http", "v1")); string(got) != string(beforeHTTP) {
		t.Fatal("invalid signer source changed HTTP destination")
	}
}

func TestSyncAtomicallyReplacesBothWholeDestinations(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "contracts", "mpc-2of3", "v1", "obsolete.json"), []byte(`{}`))
	write(t, filepath.Join(root, "testdata", "mpc-co-signer-http", "v1", "obsolete.json"), []byte(`{}`))
	signerSource := filepath.Join("..", "..", "..", "contracts", "mpc-2of3", "v1")
	httpSource := filepath.Join("..", "..", "..", "testdata", "mpc-co-signer-http", "v1")
	if err := Sync(root, signerSource, httpSource); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if _, err := VerifySignerBundle(filepath.Join(root, "contracts", "mpc-2of3", "v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHTTPBundle(filepath.Join(root, "testdata", "mpc-co-signer-http", "v1")); err != nil {
		t.Fatal(err)
	}
	if got, want := string(bundleBytes(t, filepath.Join(root, "contracts", "mpc-2of3", "v1"))), string(bundleBytes(t, signerSource)); got != want {
		t.Fatal("signer destination is not a whole source copy")
	}
	if got, want := string(bundleBytes(t, filepath.Join(root, "testdata", "mpc-co-signer-http", "v1"))), string(bundleBytes(t, httpSource)); got != want {
		t.Fatal("HTTP destination is not a whole source copy")
	}
}

func TestReplacePairRestoresSignerWhenHTTPInspectionFails(t *testing.T) {
	root := t.TempDir()
	destinationSigner := filepath.Join(root, "signer")
	destinationHTTP := filepath.Join(root, "http")
	stagedSigner := filepath.Join(root, "staged-signer")
	stagedHTTP := filepath.Join(root, "staged-http")
	write(t, filepath.Join(destinationSigner, "old"), []byte("signer"))
	write(t, filepath.Join(destinationHTTP, "old"), []byte("http"))
	write(t, filepath.Join(stagedSigner, "new"), []byte("signer"))
	write(t, filepath.Join(stagedHTTP, "new"), []byte("http"))
	beforeSigner, beforeHTTP := bundleBytes(t, destinationSigner), bundleBytes(t, destinationHTTP)
	filesystem := osPairFilesystem()
	filesystem.lstat = func(path string) (os.FileInfo, error) {
		if path == destinationHTTP {
			return nil, errors.New("injected HTTP inspection failure")
		}
		return os.Lstat(path)
	}
	if _, err := replacePairWithFilesystem(destinationSigner, stagedSigner, destinationHTTP, stagedHTTP, root, filesystem); err == nil {
		t.Fatal("replacePairWithFilesystem() error = nil")
	}
	if got := bundleBytes(t, destinationSigner); string(got) != string(beforeSigner) {
		t.Fatal("signer destination was not restored")
	}
	if got := bundleBytes(t, destinationHTTP); string(got) != string(beforeHTTP) {
		t.Fatal("HTTP destination changed")
	}
}

func TestSyncRestoresBothDestinationsWhenFinalVerificationFails(t *testing.T) {
	root := t.TempDir()
	signerSource := filepath.Join("..", "..", "..", "contracts", "mpc-2of3", "v1")
	httpSource := filepath.Join("..", "..", "..", "testdata", "mpc-co-signer-http", "v1")
	if err := Sync(root, signerSource, httpSource); err != nil {
		t.Fatal(err)
	}
	destinationSigner := filepath.Join(root, "contracts", "mpc-2of3", "v1")
	destinationHTTP := filepath.Join(root, "testdata", "mpc-co-signer-http", "v1")
	beforeSigner, beforeHTTP := bundleBytes(t, destinationSigner), bundleBytes(t, destinationHTTP)
	verifier := contractVerifier{
		verifySigner: VerifySignerBundle,
		verifyHTTP: func(path string) (string, error) {
			if path == destinationHTTP {
				return "", errors.New("injected final verification failure")
			}
			return VerifyHTTPBundle(path)
		},
	}
	if err := syncWith(root, signerSource, httpSource, verifier, osPairFilesystem()); err == nil {
		t.Fatal("syncWith() error = nil")
	}
	if got := bundleBytes(t, destinationSigner); string(got) != string(beforeSigner) {
		t.Fatal("signer destination was not restored after final verification failure")
	}
	if got := bundleBytes(t, destinationHTTP); string(got) != string(beforeHTTP) {
		t.Fatal("HTTP destination was not restored after final verification failure")
	}
}

func TestSyncPreservesBackupStagingWhenRollbackRestoreFails(t *testing.T) {
	root := t.TempDir()
	signerSource := filepath.Join("..", "..", "..", "contracts", "mpc-2of3", "v1")
	httpSource := filepath.Join("..", "..", "..", "testdata", "mpc-co-signer-http", "v1")
	if err := Sync(root, signerSource, httpSource); err != nil {
		t.Fatal(err)
	}
	destinationHTTP := filepath.Join(root, "testdata", "mpc-co-signer-http", "v1")
	filesystem := osPairFilesystem()
	filesystem.lstat = func(path string) (os.FileInfo, error) {
		if path == destinationHTTP {
			return nil, errors.New("injected HTTP inspection failure")
		}
		return os.Lstat(path)
	}
	filesystem.rename = func(from, to string) error {
		if strings.HasSuffix(filepath.ToSlash(from), "/backup-signer-v1") && strings.HasSuffix(filepath.ToSlash(to), "/contracts/mpc-2of3/v1") {
			return errors.New("injected signer restore failure")
		}
		return os.Rename(from, to)
	}
	verifier := contractVerifier{verifySigner: VerifySignerBundle, verifyHTTP: VerifyHTTPBundle}
	err := syncWith(root, signerSource, httpSource, verifier, filesystem)
	if err == nil {
		t.Fatal("syncWith() error = nil")
	}
	if !strings.Contains(err.Error(), "injected signer restore failure") || !strings.Contains(err.Error(), "preserved rollback staging:") {
		t.Fatalf("syncWith() error = %v", err)
	}
	entries, readErr := filepath.Glob(filepath.Join(root, ".contract-sync-*", "backup-signer-v1"))
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("rollback backup was not preserved: entries=%v error=%v", entries, readErr)
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

func bundleBytes(t *testing.T, root string) []byte {
	t.Helper()
	var out []byte
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, []byte(path[len(root):])...)
		out = append(out, contents...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
