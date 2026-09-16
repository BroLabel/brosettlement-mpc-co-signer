package main

import "runtime/debug"

const localRevisionLength = 12

type buildIdentity struct {
	version  string
	revision string
}

func currentBuildIdentity() buildIdentity {
	var vcsRevision string
	var vcsModified bool
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				vcsRevision = setting.Value
			case "vcs.modified":
				vcsModified = setting.Value == "true"
			}
		}
	}

	return resolveBuildIdentity(version, revision, vcsRevision, vcsModified)
}

func resolveBuildIdentity(
	configuredVersion,
	configuredRevision,
	vcsRevision string,
	vcsModified bool,
) buildIdentity {
	identity := buildIdentity{
		version:  configuredVersion,
		revision: configuredRevision,
	}
	if identity.revision == "unknown" && vcsRevision != "" {
		identity.revision = vcsRevision
	}
	if identity.version != "dev" || identity.revision == "unknown" {
		return identity
	}

	shortRevision := identity.revision
	if len(shortRevision) > localRevisionLength {
		shortRevision = shortRevision[:localRevisionLength]
	}
	identity.version = "dev+" + shortRevision
	if vcsModified {
		identity.version += ".dirty"
	}
	return identity
}
