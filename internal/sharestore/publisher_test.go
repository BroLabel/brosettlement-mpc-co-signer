package sharestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
)

func TestPublishEntropyFailurePrecedesFilesystemAccess(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	publishCalls := 0
	store.publishSupported = func() bool { return true }
	store.nonceSource = errorReader{}
	store.publishFile = func(string, []byte) error {
		publishCalls++
		return nil
	}
	_, err := store.PublishAndInspect(context.Background(), PublishInput{
		SessionID: testSessionID, KeyID: testKeyID, PartyID: primaryPartyID,
		DescriptorBytes: testDescriptor(t, testKeyID), CodecBlob: testCodecBlob(t),
	})
	if err == nil {
		t.Fatal("PublishAndInspect() error = nil")
	}
	if publishCalls != 0 {
		t.Fatalf("filesystem publish calls = %d, want 0", publishCalls)
	}
}

func TestPublishEvidenceComesFromExactReadbackBytes(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	descriptor := testDescriptor(t, testKeyID)
	input := PublishInput{
		SessionID: testSessionID, KeyID: testKeyID, PartyID: primaryPartyID,
		DescriptorBytes: descriptor, CodecBlob: testCodecBlob(t),
	}
	readback, err := encodeArtifactV1(store.config, input, bytes.Repeat([]byte{0x77}, artifactNonceBytes))
	if err != nil {
		t.Fatal(err)
	}
	var published []byte
	store.publishSupported = func() bool { return true }
	store.nonceSource = bytes.NewReader(bytes.Repeat([]byte{0x66}, artifactNonceBytes))
	store.publishFile = func(_ string, bytes []byte) error {
		published = append([]byte(nil), bytes...)
		return nil
	}
	store.readFile = func(string) ([]byte, error) {
		return append([]byte(nil), readback...), nil
	}

	evidence, err := store.PublishAndInspect(context.Background(), input)
	if err != nil {
		t.Fatalf("PublishAndInspect() error = %v", err)
	}
	if bytes.Equal(published, readback) {
		t.Fatal("test setup did not distinguish encoded bytes from readback bytes")
	}
	if evidence.ArtifactFingerprint != mpc2of3.ArtifactFingerprintFor(readback) {
		t.Fatal("artifact fingerprint did not use exact readback bytes")
	}
}

func TestPublishCapabilityProbeProvesNoReplaceAndDurableCleanup(t *testing.T) {
	operations := newProbePublishOperations()
	err := probePublishCapability(
		operations,
		operations.read,
		"/store",
		bytes.NewReader(bytes.Repeat([]byte{0x42}, capabilityProbeEntropyBytes)),
	)
	if err != nil {
		t.Fatalf("probePublishCapability() error = %v", err)
	}
	if len(operations.files) != 0 {
		t.Fatalf("probe left files behind: %v", operations.files)
	}
	if operations.renameCalls != 2 {
		t.Fatalf("rename calls = %d, want 2", operations.renameCalls)
	}
	if operations.directorySyncCalls != 2 {
		t.Fatalf("directory sync calls = %d, want publish and cleanup syncs", operations.directorySyncCalls)
	}
}

func TestPublishCapabilityProbeFailsUnsupportedRenameAndCleansTemp(t *testing.T) {
	operations := newProbePublishOperations()
	operations.renameErr = errors.New("renameat2 unavailable")
	err := probePublishCapability(
		operations,
		operations.read,
		"/store",
		bytes.NewReader(bytes.Repeat([]byte{0x42}, capabilityProbeEntropyBytes)),
	)
	if err == nil {
		t.Fatal("probePublishCapability() error = nil")
	}
	if len(operations.files) != 0 {
		t.Fatalf("failed rename probe left files behind: %v", operations.files)
	}
}

func TestPublishCapabilityProbeFailsDirectorySyncAndDurablyCleansFinal(t *testing.T) {
	operations := newProbePublishOperations()
	operations.failDirectorySyncCall = 1
	err := probePublishCapability(
		operations,
		operations.read,
		"/store",
		bytes.NewReader(bytes.Repeat([]byte{0x42}, capabilityProbeEntropyBytes)),
	)
	if err == nil {
		t.Fatal("probePublishCapability() error = nil")
	}
	if len(operations.files) != 0 {
		t.Fatalf("failed directory-sync probe left files behind: %v", operations.files)
	}
	if operations.directorySyncCalls != 2 {
		t.Fatalf("directory sync calls = %d, want failed publish sync plus cleanup sync", operations.directorySyncCalls)
	}
}

func TestPublishCapabilityProbeRejectsReplacingRenameImplementation(t *testing.T) {
	operations := newProbePublishOperations()
	operations.replaceExisting = true
	err := probePublishCapability(
		operations,
		operations.read,
		"/store",
		bytes.NewReader(bytes.Repeat([]byte{0x42}, capabilityProbeEntropyBytes)),
	)
	if err == nil {
		t.Fatal("probePublishCapability() accepted replacing rename")
	}
	if len(operations.files) != 0 {
		t.Fatalf("replacement probe left files behind: %v", operations.files)
	}
}

func TestDurablePublisherUsesRequiredOrder(t *testing.T) {
	recorder := &recordingPublishOps{}
	if err := publishDurably(recorder, "/store/key.primary.json", []byte("artifact")); err != nil {
		t.Fatalf("publishDurably() error = %v", err)
	}
	want := []string{
		"create-temp", "chmod-0600", "write", "file-sync", "close",
		"rename-noreplace", "directory-sync",
	}
	if !reflect.DeepEqual(recorder.calls, want) {
		t.Fatalf("calls = %v, want %v", recorder.calls, want)
	}
}

func TestDurablePublisherStopsAtEveryFailedBoundary(t *testing.T) {
	boundaries := []string{
		"create-temp", "chmod-0600", "write", "file-sync", "close",
		"rename-noreplace", "directory-sync",
	}
	for _, boundary := range boundaries {
		t.Run(boundary, func(t *testing.T) {
			recorder := &recordingPublishOps{failAt: boundary}
			if err := publishDurably(recorder, "/store/key.primary.json", []byte("artifact")); err == nil {
				t.Fatal("publishDurably() error = nil")
			}
			position := -1
			for index, call := range recorder.calls {
				if call == boundary {
					position = index
					break
				}
			}
			if position < 0 {
				t.Fatalf("failed boundary %q was not called: %v", boundary, recorder.calls)
			}
			for _, later := range recorder.calls[position+1:] {
				if later != "close" {
					t.Fatalf("security operation %q continued after %q: %v", later, boundary, recorder.calls)
				}
			}
			if boundary != "create-temp" && boundary != "directory-sync" && !recorder.removed {
				t.Fatalf("temporary file not cleaned after %q", boundary)
			}
			if boundary == "directory-sync" && recorder.removed {
				t.Fatal("published final file was treated as removable temp")
			}
		})
	}
}

type recordingPublishOps struct {
	calls   []string
	failAt  string
	removed bool
	file    recordingPublishFile
}

func (r *recordingPublishOps) CreateTemp(string, string) (publishTempFile, error) {
	r.calls = append(r.calls, "create-temp")
	if r.failAt == "create-temp" {
		return nil, errors.New("injected create failure")
	}
	r.file.owner = r
	return &r.file, nil
}

func (r *recordingPublishOps) Remove(string) error {
	r.removed = true
	return nil
}

func (r *recordingPublishOps) RenameNoReplace(string, string) error {
	r.calls = append(r.calls, "rename-noreplace")
	if r.failAt == "rename-noreplace" {
		return errors.New("injected rename failure")
	}
	return nil
}

func (r *recordingPublishOps) SyncDirectory(string) error {
	r.calls = append(r.calls, "directory-sync")
	if r.failAt == "directory-sync" {
		return errors.New("injected directory sync failure")
	}
	return nil
}

type recordingPublishFile struct {
	owner *recordingPublishOps
}

func (f *recordingPublishFile) Name() string { return "/store/.temp" }

func (f *recordingPublishFile) Chmod(mode os.FileMode) error {
	if mode != 0o600 {
		return errors.New("unexpected mode")
	}
	f.owner.calls = append(f.owner.calls, "chmod-0600")
	return f.fail("chmod-0600")
}

func (f *recordingPublishFile) Write(bytes []byte) (int, error) {
	f.owner.calls = append(f.owner.calls, "write")
	if err := f.fail("write"); err != nil {
		return 0, err
	}
	return len(bytes), nil
}

func (f *recordingPublishFile) Sync() error {
	f.owner.calls = append(f.owner.calls, "file-sync")
	return f.fail("file-sync")
}

func (f *recordingPublishFile) Close() error {
	f.owner.calls = append(f.owner.calls, "close")
	return f.fail("close")
}

func (f *recordingPublishFile) fail(boundary string) error {
	if f.owner.failAt == boundary {
		return errors.New("injected " + boundary + " failure")
	}
	return nil
}

var _ io.Writer = (*recordingPublishFile)(nil)

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

type probePublishOperations struct {
	files                 map[string][]byte
	tempCounter           int
	renameCalls           int
	directorySyncCalls    int
	failDirectorySyncCall int
	renameErr             error
	replaceExisting       bool
}

func newProbePublishOperations() *probePublishOperations {
	return &probePublishOperations{files: make(map[string][]byte)}
}

func (o *probePublishOperations) CreateTemp(directory, _ string) (publishTempFile, error) {
	o.tempCounter++
	path := filepath.Join(directory, fmt.Sprintf(".probe-temp-%d", o.tempCounter))
	return &probePublishFile{owner: o, path: path}, nil
}

func (o *probePublishOperations) Remove(path string) error {
	delete(o.files, path)
	return nil
}

func (o *probePublishOperations) RenameNoReplace(oldPath, newPath string) error {
	o.renameCalls++
	if o.renameErr != nil {
		return o.renameErr
	}
	if _, exists := o.files[newPath]; exists && !o.replaceExisting {
		return ErrArtifactExists
	}
	o.files[newPath] = append([]byte(nil), o.files[oldPath]...)
	delete(o.files, oldPath)
	return nil
}

func (o *probePublishOperations) SyncDirectory(string) error {
	o.directorySyncCalls++
	if o.directorySyncCalls == o.failDirectorySyncCall {
		return errors.New("directory fsync unavailable")
	}
	return nil
}

func (o *probePublishOperations) read(path string) ([]byte, error) {
	bytes, ok := o.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), bytes...), nil
}

type probePublishFile struct {
	owner  *probePublishOperations
	path   string
	bytes  []byte
	closed bool
}

func (f *probePublishFile) Name() string { return f.path }

func (f *probePublishFile) Chmod(mode os.FileMode) error {
	if mode != artifactFilePerm {
		return errors.New("unexpected probe mode")
	}
	return nil
}

func (f *probePublishFile) Write(bytes []byte) (int, error) {
	f.bytes = append(f.bytes, bytes...)
	return len(bytes), nil
}

func (f *probePublishFile) Sync() error { return nil }

func (f *probePublishFile) Close() error {
	if f.closed {
		return errors.New("probe file closed twice")
	}
	f.closed = true
	f.owner.files[f.path] = append([]byte(nil), f.bytes...)
	return nil
}
