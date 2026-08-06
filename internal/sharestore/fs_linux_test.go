//go:build linux

package sharestore

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

func TestLinuxPublishIsCreateOnlyDurableAndReadable(t *testing.T) {
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
	if info.Mode().Perm() != 0o600 {
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

func TestLinuxPublishCapabilityProbeLeavesNoFiles(t *testing.T) {
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

func TestLinuxStoreDirectoryMustBeOwnedByProcessUser(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wrongOwner := uint32(os.Geteuid() + 1)
	foreign := fileInfoWithSys{
		FileInfo: info,
		sys:      &syscall.Stat_t{Uid: wrongOwner},
	}
	if err := validateStoreDirectoryOwner(foreign); err == nil {
		t.Fatal("validateStoreDirectoryOwner() accepted foreign owner")
	}
}

type fileInfoWithSys struct {
	os.FileInfo
	sys any
}

func (i fileInfoWithSys) Sys() any { return i.sys }

func TestLinuxPublishDoesNotFollowExistingFinalSymlink(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	target := filepath.Join(t.TempDir(), "target")
	original := []byte("do-not-change")
	if err := os.WriteFile(target, original, 0o600); err != nil {
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

func TestLinuxWrongKeyAndKeyRefFailClosed(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	store.nonceSource = bytes.NewReader(bytes.Repeat([]byte{0x55}, artifactNonceBytes))
	descriptor := testDescriptor(t, testKeyID)
	input := PublishInput{
		SessionID: testSessionID, KeyID: testKeyID, PartyID: primaryPartyID,
		DescriptorBytes: descriptor, CodecBlob: testCodecBlob(t),
	}
	if _, err := store.PublishAndInspect(context.Background(), input); err != nil {
		t.Fatal(err)
	}

	wrongProvider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x02}, 32)), testKeyRef)
	if err != nil {
		t.Fatal(err)
	}
	wrongConfig, err := NewStoreConfig(StorePurposePrimary, primaryPartyID, store.config.Directory(), wrongProvider)
	if err != nil {
		t.Fatal(err)
	}
	wrongStore, err := NewStore(wrongConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongStore.InspectExisting(context.Background(), ExpectedArtifactContext{
		SessionID: testSessionID, KeyID: testKeyID, DescriptorBytes: descriptor,
	}); !errors.Is(err, coretss.ErrInvalidSharePayload) {
		t.Fatalf("wrong-key InspectExisting() error = %v", err)
	}

	otherRefProvider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 32)), "other-ref")
	if err != nil {
		t.Fatal(err)
	}
	otherRefConfig, err := NewStoreConfig(StorePurposePrimary, primaryPartyID, store.config.Directory(), otherRefProvider)
	if err != nil {
		t.Fatal(err)
	}
	otherRefStore, err := NewStore(otherRefConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherRefStore.InspectExisting(context.Background(), ExpectedArtifactContext{
		SessionID: testSessionID, KeyID: testKeyID, DescriptorBytes: descriptor,
	}); !errors.Is(err, ErrArtifactBinding) {
		t.Fatalf("wrong-keyRef InspectExisting() error = %v", err)
	}
}

func TestLinuxRoutingWriterPublishesOnlyTheRegisteredPurpose(t *testing.T) {
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 32)), testKeyRef)
	if err != nil {
		t.Fatal(err)
	}
	primary := testStoreWithProvider(t, StorePurposePrimary, provider)
	recovery := testStoreWithProvider(t, StorePurposeRecovery, provider)
	primary.nonceSource = bytes.NewReader(bytes.Repeat([]byte{0x31}, artifactNonceBytes))
	recovery.nonceSource = bytes.NewReader(bytes.Repeat([]byte{0x32}, artifactNonceBytes))
	slot := NewActivePair()
	descriptor := testDescriptor(t, testKeyID)
	lease, err := slot.RegisterPair(PairRegistration{
		SessionID: testSessionID, KeyID: testKeyID,
		PrimaryPartyID: primaryPartyID, RecoveryPartyID: recoveryPartyID,
		DescriptorBytes: descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	writer, err := NewRoutingWriter(slot, primary, recovery)
	if err != nil {
		t.Fatal(err)
	}
	for _, partyID := range []string{primaryPartyID, recoveryPartyID} {
		if err := writer.SaveShare(context.Background(), coretss.SaveShareInput{
			SessionID: testSessionID, KeyID: testKeyID, LocalPartyID: partyID,
			OpaqueDescriptorFingerprint: fingerprintBytes(descriptor),
			CodecBlob:                   testCodecBlob(t),
		}); err != nil {
			t.Fatalf("SaveShare(%s) error = %v", partyID, err)
		}
	}
	if _, err := primary.InspectExisting(context.Background(), ExpectedArtifactContext{
		SessionID: testSessionID, KeyID: testKeyID, DescriptorBytes: descriptor,
	}); err != nil {
		t.Fatalf("inspect primary: %v", err)
	}
	if _, err := recovery.InspectExisting(context.Background(), ExpectedArtifactContext{
		SessionID: testSessionID, KeyID: testKeyID, DescriptorBytes: descriptor,
	}); err != nil {
		t.Fatalf("inspect recovery: %v", err)
	}
}

func TestLinuxPublishCrashBoundaries(t *testing.T) {
	boundaries := []string{"create-temp", "write", "file-sync", "close", "rename-noreplace", "directory-sync"}
	for _, boundary := range boundaries {
		t.Run(boundary, func(t *testing.T) {
			directory := t.TempDir()
			command := exec.Command(os.Args[0], "-test.run", "^TestLinuxPublishCrashHelper$")
			command.Env = append(os.Environ(),
				"MPC_ARTIFACT_CRASH_HELPER=1",
				"MPC_ARTIFACT_CRASH_DIR="+directory,
				"MPC_ARTIFACT_CRASH_AT="+boundary,
			)
			err := command.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
				t.Fatalf("helper error = %v", err)
			}
			finalPath := filepath.Join(directory, "key.primary.json")
			_, statErr := os.Lstat(finalPath)
			published := statErr == nil
			wantPublished := boundary == "rename-noreplace" || boundary == "directory-sync"
			if published != wantPublished {
				t.Fatalf("final published = %v, want %v (stat error %v)", published, wantPublished, statErr)
			}
		})
	}
}

func TestLinuxPublishCrashHelper(t *testing.T) {
	if os.Getenv("MPC_ARTIFACT_CRASH_HELPER") != "1" {
		t.Skip("helper process only")
	}
	directory := os.Getenv("MPC_ARTIFACT_CRASH_DIR")
	boundary := os.Getenv("MPC_ARTIFACT_CRASH_AT")
	finalPath := filepath.Join(directory, "key.primary.json")
	operations := crashPublishOperations{inner: realPublishOperations{}, boundary: boundary}
	_ = publishDurably(operations, finalPath, []byte("artifact"))
	t.Fatalf("publish completed without crash at %q", boundary)
}

type crashPublishOperations struct {
	inner    realPublishOperations
	boundary string
}

func (o crashPublishOperations) CreateTemp(directory, pattern string) (publishTempFile, error) {
	file, err := o.inner.CreateTemp(directory, pattern)
	if err == nil && o.boundary == "create-temp" {
		os.Exit(23)
	}
	return crashPublishFile{publishTempFile: file, boundary: o.boundary}, err
}

func (o crashPublishOperations) Remove(path string) error { return o.inner.Remove(path) }

func (o crashPublishOperations) RenameNoReplace(oldPath, newPath string) error {
	err := o.inner.RenameNoReplace(oldPath, newPath)
	if err == nil && o.boundary == "rename-noreplace" {
		os.Exit(23)
	}
	return err
}

func (o crashPublishOperations) SyncDirectory(directory string) error {
	err := o.inner.SyncDirectory(directory)
	if err == nil && o.boundary == "directory-sync" {
		os.Exit(23)
	}
	return err
}

type crashPublishFile struct {
	publishTempFile
	boundary string
}

func (f crashPublishFile) Write(bytes []byte) (int, error) {
	written, err := f.publishTempFile.Write(bytes)
	if err == nil && f.boundary == "write" {
		os.Exit(23)
	}
	return written, err
}

func (f crashPublishFile) Sync() error {
	err := f.publishTempFile.Sync()
	if err == nil && f.boundary == "file-sync" {
		os.Exit(23)
	}
	return err
}

func (f crashPublishFile) Close() error {
	err := f.publishTempFile.Close()
	if err == nil && f.boundary == "close" {
		os.Exit(23)
	}
	return err
}
