package metrics

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
