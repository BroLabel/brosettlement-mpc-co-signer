package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncRejectsRepositoryRootOverride(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	arbitraryDestination := t.TempDir()
	command := exec.Command("go", "run", "./cmd/mpc-contracts", "sync",
		"--repository-root", arbitraryDestination,
		"--contract-source", filepath.Join(repositoryRoot, "contracts", "mpc-2of3", "v1"),
		"--http-source", filepath.Join(repositoryRoot, "testdata", "mpc-co-signer-http", "v1"),
	)
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("sync accepted arbitrary repository root: %s", output)
	}
	if !strings.Contains(string(output), "flag provided but not defined: -repository-root") {
		t.Fatalf("sync output = %q", output)
	}
	if entries, readErr := os.ReadDir(arbitraryDestination); readErr != nil || len(entries) != 0 {
		t.Fatalf("arbitrary destination changed: entries=%v error=%v", entries, readErr)
	}
}
