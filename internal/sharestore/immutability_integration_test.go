//go:build linux

// This store-bound proof composes with terminal.Publisher's real lost-response
// EXACT_REPLAY tests and reconcile's own-CLAIMED reconstruction matrix: those
// components may inspect addressed artifacts but never rewrite their bytes.

package sharestore

import (
	"bytes"
	"context"
	"os"
	"testing"
)

type immutableArtifactSnapshot struct {
	bytes []byte
	mode  os.FileMode
	mtime int64
}

func TestPublishedArtifactsRemainImmutableAcrossPrimaryLoadTerminalReplayAndClaimedReconstruction(t *testing.T) {
	primary := testStore(t, StorePurposePrimary)
	recovery := testStore(t, StorePurposeRecovery)
	descriptor := testDescriptor(t, testKeyID)
	primaryInput := PublishInput{SessionID: testSessionID, KeyID: testKeyID, PartyID: primaryPartyID, DescriptorBytes: descriptor, CodecBlob: testCodecBlob(t)}
	recoveryInput := PublishInput{SessionID: testSessionID, KeyID: testKeyID, PartyID: recoveryPartyID, DescriptorBytes: descriptor, CodecBlob: testCodecBlob(t)}
	if _, err := primary.PublishAndInspect(context.Background(), primaryInput); err != nil {
		t.Fatalf("publish B: %v", err)
	}
	if _, err := recovery.PublishAndInspect(context.Background(), recoveryInput); err != nil {
		t.Fatalf("publish C: %v", err)
	}
	beforeB := snapshotImmutableArtifact(t, primary, testKeyID)
	beforeC := snapshotImmutableArtifact(t, recovery, testKeyID)

	// Normal A+B production access uses only the primary reader.
	reader, err := NewPrimaryReader(primary)
	if err != nil {
		t.Fatal(err)
	}
	share, err := reader.LoadShare(context.Background(), testKeyID)
	if err != nil {
		t.Fatalf("primary load: %v", err)
	}
	clear(share.Blob)

	// The store path used by terminal replay is a read-only addressed inspection.
	expected := ExpectedArtifactContext{SessionID: testSessionID, KeyID: testKeyID, DescriptorBytes: descriptor}
	if _, err := primary.InspectExisting(context.Background(), expected); err != nil {
		t.Fatalf("terminal replay B: %v", err)
	}
	if _, err := recovery.InspectExisting(context.Background(), expected); err != nil {
		t.Fatalf("terminal replay C: %v", err)
	}
	// Own-CLAIMED reconstruction uses the same strict addressed inspections.
	if _, err := primary.InspectExisting(context.Background(), expected); err != nil {
		t.Fatalf("claimed reconstruction B: %v", err)
	}
	if _, err := recovery.InspectExisting(context.Background(), expected); err != nil {
		t.Fatalf("claimed reconstruction C: %v", err)
	}

	assertImmutableArtifact(t, primary, testKeyID, beforeB)
	assertImmutableArtifact(t, recovery, testKeyID, beforeC)
}

func snapshotImmutableArtifact(t *testing.T, store *Store, keyID string) immutableArtifactSnapshot {
	t.Helper()
	path, err := store.finalPath(keyID)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return immutableArtifactSnapshot{bytes: bytes, mode: info.Mode(), mtime: info.ModTime().UnixNano()}
}

func assertImmutableArtifact(t *testing.T, store *Store, keyID string, before immutableArtifactSnapshot) {
	t.Helper()
	after := snapshotImmutableArtifact(t, store, keyID)
	if !bytes.Equal(after.bytes, before.bytes) || after.mode != before.mode || after.mtime != before.mtime {
		t.Fatal("artifact bytes, mode, or mtime changed")
	}
}
