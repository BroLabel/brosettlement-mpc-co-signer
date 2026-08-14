package metrics

func SetTerminalUnconfirmed(unconfirmed bool) {
	Default.Set("dkg_terminal_unconfirmed", nil, boolFloat(unconfirmed))
}
func ObserveTerminalAttempt()  { Default.Inc("dkg_terminal_publish_attempts_total", nil) }
func ObserveTerminalConflict() { Default.Inc("dkg_terminal_conflicts_total", nil) }
