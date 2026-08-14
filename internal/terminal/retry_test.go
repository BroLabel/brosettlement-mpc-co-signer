package terminal

import (
	"testing"
	"time"
)

func TestExponentialRetryPolicyUsesDistinctCappedTransientAndProtocolPaths(t *testing.T) {
	policy, err := NewExponentialRetryPolicy(RetryConfig{
		InitialDelay:     10 * time.Millisecond,
		MaxDelay:         40 * time.Millisecond,
		ProtocolDelay:    100 * time.Millisecond,
		MaxProtocolDelay: 250 * time.Millisecond,
		JitterFraction:   0,
		RandomFloat64:    func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	for attempt, want := range []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
		40 * time.Millisecond,
		40 * time.Millisecond,
	} {
		if got := policy.NextDelay(uint64(attempt+1), RetryTransient); got != want {
			t.Fatalf("transient attempt %d delay = %s, want %s", attempt+1, got, want)
		}
	}
	for attempt, want := range []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		250 * time.Millisecond,
		250 * time.Millisecond,
	} {
		if got := policy.NextDelay(uint64(attempt+1), RetryProtocol); got != want {
			t.Fatalf("protocol attempt %d delay = %s, want %s", attempt+1, got, want)
		}
	}
	if got := policy.NextDelay(^uint64(0), RetryProtocol); got != 250*time.Millisecond {
		t.Fatalf("overflow attempt delay = %s, want capped delay", got)
	}
}

func TestExponentialRetryPolicyJitterNeverExceedsCap(t *testing.T) {
	policy, err := NewExponentialRetryPolicy(RetryConfig{
		InitialDelay:     time.Second,
		MaxDelay:         2 * time.Second,
		ProtocolDelay:    3 * time.Second,
		MaxProtocolDelay: 4 * time.Second,
		JitterFraction:   0.5,
		RandomFloat64:    func() float64 { return 1 },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.NextDelay(20, RetryTransient); got != 2*time.Second {
		t.Fatalf("transient jittered cap = %s", got)
	}
	if got := policy.NextDelay(20, RetryProtocol); got != 4*time.Second {
		t.Fatalf("protocol jittered cap = %s", got)
	}
}
