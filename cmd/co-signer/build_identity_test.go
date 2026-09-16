package main

import "testing"

func TestResolveBuildIdentityDerivesLocalVersionFromVCS(t *testing.T) {
	const fullRevision = "0123456789abcdef0123456789abcdef01234567"

	identity := resolveBuildIdentity("dev", "unknown", fullRevision, false)

	if identity.version != "dev+0123456789ab" {
		t.Fatalf("version = %q, want %q", identity.version, "dev+0123456789ab")
	}
	if identity.revision != fullRevision {
		t.Fatalf("revision = %q, want %q", identity.revision, fullRevision)
	}
}

func TestResolveBuildIdentityMarksModifiedCheckout(t *testing.T) {
	identity := resolveBuildIdentity("dev", "unknown", "abcdef0123456789", true)

	if identity.version != "dev+abcdef012345.dirty" {
		t.Fatalf("version = %q, want %q", identity.version, "dev+abcdef012345.dirty")
	}
}

func TestResolveBuildIdentityPreservesReleaseMetadata(t *testing.T) {
	identity := resolveBuildIdentity("2.0.0", "release-revision", "checkout-revision", true)

	if identity.version != "2.0.0" {
		t.Fatalf("version = %q, want %q", identity.version, "2.0.0")
	}
	if identity.revision != "release-revision" {
		t.Fatalf("revision = %q, want %q", identity.revision, "release-revision")
	}
}

func TestResolveBuildIdentityKeepsFallbacksWithoutVCSMetadata(t *testing.T) {
	identity := resolveBuildIdentity("dev", "unknown", "", false)

	if identity.version != "dev" || identity.revision != "unknown" {
		t.Fatalf("identity = %#v, want dev/unknown", identity)
	}
}
