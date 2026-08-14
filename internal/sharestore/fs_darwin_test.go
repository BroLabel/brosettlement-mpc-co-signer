//go:build darwin

package sharestore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
)

func TestDarwinPublishIsCreateOnlyDurableAndReadable(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	store.nonceSource = bytes.NewReader(bytes.Repeat([]byte{0x55}, artifactNonceBytes))
	descriptor := testDescriptor(t, testKeyID)
	input := PublishInput{
		SessionID: testSessionID, KeyID: testKeyID, PartyID: primaryPartyID,
		DescriptorBytes: descriptor, CodecBlob: testCodecBlob(t),
	}
	evidence, err := store.PublishAndInspect(context.Background(), input)
	if err != nil {
		t.Fatalf("PublishAndInspect() error = %v", err)
	}
	path, err := store.finalPath(testKeyID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != artifactFilePerm {
		t.Fatalf("artifact mode = %o", info.Mode().Perm())
	}
	if evidence.ArtifactFingerprint != mpc2of3.ArtifactFingerprintFor(before) {
		t.Fatal("fingerprint does not hash exact final bytes")
	}

	store.nonceSource = bytes.NewReader(bytes.Repeat([]byte{0x66}, artifactNonceBytes))
	if _, err := store.PublishAndInspect(context.Background(), input); !errors.Is(err, ErrArtifactExists) {
		t.Fatalf("second PublishAndInspect() error = %v, want ErrArtifactExists", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("existing immutable artifact changed")
	}

	reader, err := NewPrimaryReader(store)
	if err != nil {
		t.Fatal(err)
	}
	share, err := reader.LoadShare(context.Background(), testKeyID)
	if err != nil {
		t.Fatalf("LoadShare() error = %v", err)
	}
	defer clear(share.Blob)
	if !bytes.Equal(share.Blob, input.CodecBlob) {
		t.Fatal("primary reader returned the wrong codec blob")
	}
}

func TestDarwinPublishCapabilityProbeLeavesNoFiles(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	if err := store.ProbePublishCapability(context.Background()); err != nil {
		t.Fatalf("ProbePublishCapability() error = %v", err)
	}
	entries, err := os.ReadDir(store.config.Directory())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("capability probe left entries: %v", entries)
	}
}

func TestDarwinStoreDirectoryMustBeOwnedByProcessUser(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wrongOwner := uint32(os.Geteuid() + 1)
	foreign := darwinFileInfoWithSys{
		FileInfo: info,
		sys:      &syscall.Stat_t{Uid: wrongOwner},
	}
	if err := validateStoreDirectoryOwner(foreign); err == nil {
		t.Fatal("validateStoreDirectoryOwner() accepted foreign owner")
	}
}

type darwinFileInfoWithSys struct {
	os.FileInfo
	sys any
}

func (i darwinFileInfoWithSys) Sys() any { return i.sys }

func TestDarwinPublishDoesNotFollowExistingFinalSymlink(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	target := filepath.Join(t.TempDir(), "target")
	original := []byte("do-not-change")
	if err := os.WriteFile(target, original, artifactFilePerm); err != nil {
		t.Fatal(err)
	}
	finalPath, err := store.finalPath(testKeyID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, finalPath); err != nil {
		t.Fatal(err)
	}
	store.nonceSource = bytes.NewReader(bytes.Repeat([]byte{0x55}, artifactNonceBytes))
	_, err = store.PublishAndInspect(context.Background(), PublishInput{
		SessionID: testSessionID, KeyID: testKeyID, PartyID: primaryPartyID,
		DescriptorBytes: testDescriptor(t, testKeyID), CodecBlob: testCodecBlob(t),
	})
	if !errors.Is(err, ErrArtifactExists) {
		t.Fatalf("PublishAndInspect() error = %v, want ErrArtifactExists", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("symlink target changed")
	}
}
