package metrics

func ObserveArtifactPublish(success bool) {
	Default.Inc("artifact_publish_total", nil)
	if !success {
		Default.Inc("artifact_publish_failures_total", nil)
	}
}
func ObserveArtifactInspection(success bool) {
	if !success {
		Default.Inc("artifact_inspection_failures_total", nil)
	}
}
func ObserveArtifactInventory(files, temporary, bytes, oldestAge, freeBytes float64) {
	Default.Set("artifact_file_count", nil, files)
	Default.Set("artifact_temporary_file_count", nil, temporary)
	Default.Set("artifact_total_bytes", nil, bytes)
	Default.Set("artifact_oldest_file_age_seconds", nil, oldestAge)
	Default.Set("artifact_filesystem_free_bytes", nil, freeBytes)
}
