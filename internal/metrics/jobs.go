package metrics

func SetJobCapacity(capacity int) {
	if capacity > 0 {
		Default.Set("co_signer_job_capacity", nil, float64(capacity))
	}
}

func ObserveAdmission(kind, outcome string) {
	Default.Inc("co_signer_admission_total", Labels{"type": kind, "outcome": outcome})
}

func JobStarted(kind string) {
	if kind == "DKG" {
		Default.Add("active_dkg_jobs", nil, 1)
	}
	if kind == "SIGN" {
		Default.Add("active_sign_jobs", nil, 1)
	}
}

func JobFinished(kind string) {
	if kind == "DKG" {
		Default.Add("active_dkg_jobs", nil, -1)
	}
	if kind == "SIGN" {
		Default.Add("active_sign_jobs", nil, -1)
	}
}

func ObserveSessionDuration(kind string, seconds float64) {
	Default.Observe("mpc_session_duration_seconds", Labels{"type": kind}, seconds)
}
