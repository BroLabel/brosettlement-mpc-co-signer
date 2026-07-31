package metrics

// Filesystem inventory is supplied as aggregate values only. The collector is
// forbidden from returning paths, file names, key IDs, or terminal guesses.
type FilesystemInventory struct {
	ArtifactFiles, TemporaryFiles, TotalBytes uint64
	OldestAgeSeconds, FreeBytes               float64
}

func ObserveFilesystemInventory(inventory FilesystemInventory) {
	ObserveArtifactInventory(float64(inventory.ArtifactFiles), float64(inventory.TemporaryFiles), float64(inventory.TotalBytes), inventory.OldestAgeSeconds, inventory.FreeBytes)
}
