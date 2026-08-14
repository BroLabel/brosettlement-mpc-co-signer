package terminal

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"math"
	"time"
)

type RetryKind uint8

const (
	RetryTransient RetryKind = iota + 1
	RetryProtocol
)

type RetryPolicy interface {
	NextDelay(attempt uint64, kind RetryKind) time.Duration
}

type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

type RetryConfig struct {
	InitialDelay     time.Duration
	MaxDelay         time.Duration
	ProtocolDelay    time.Duration
	MaxProtocolDelay time.Duration
	JitterFraction   float64
	RandomFloat64    func() float64
}

type ExponentialRetryPolicy struct {
	config RetryConfig
}

func NewExponentialRetryPolicy(config RetryConfig) (*ExponentialRetryPolicy, error) {
	if config.InitialDelay <= 0 || config.MaxDelay < config.InitialDelay ||
		config.ProtocolDelay <= 0 || config.MaxProtocolDelay < config.ProtocolDelay {
		return nil, errors.New("terminal retry delays must be positive and capped")
	}
	if config.JitterFraction < 0 || config.JitterFraction > 1 {
		return nil, errors.New("terminal retry jitter must be between zero and one")
	}
	if config.RandomFloat64 == nil {
		config.RandomFloat64 = cryptoFloat64
	}
	return &ExponentialRetryPolicy{config: config}, nil
}

func DefaultRetryPolicy() RetryPolicy {
	policy, _ := NewExponentialRetryPolicy(RetryConfig{
		InitialDelay:     250 * time.Millisecond,
		MaxDelay:         30 * time.Second,
		ProtocolDelay:    5 * time.Second,
		MaxProtocolDelay: 2 * time.Minute,
		JitterFraction:   0.2,
	})
	return policy
}

func (p *ExponentialRetryPolicy) NextDelay(attempt uint64, kind RetryKind) time.Duration {
	if p == nil {
		return 0
	}
	initial, maximum := p.config.InitialDelay, p.config.MaxDelay
	if kind == RetryProtocol {
		initial, maximum = p.config.ProtocolDelay, p.config.MaxProtocolDelay
	}
	delay := cappedExponential(initial, maximum, attempt)
	if delay <= 0 || p.config.JitterFraction == 0 {
		return delay
	}
	random := p.config.RandomFloat64()
	if random < 0 {
		random = 0
	}
	if random > 1 {
		random = 1
	}
	factor := 1 - p.config.JitterFraction + 2*p.config.JitterFraction*random
	jittered := time.Duration(float64(delay) * factor)
	if jittered > maximum {
		return maximum
	}
	if jittered < 0 {
		return 0
	}
	return jittered
}

func cappedExponential(initial, maximum time.Duration, attempt uint64) time.Duration {
	if attempt <= 1 {
		return initial
	}
	shift := attempt - 1
	if shift >= 63 || initial > maximum/time.Duration(uint64(1)<<shift) {
		return maximum
	}
	delay := initial * time.Duration(uint64(1)<<shift)
	if delay > maximum {
		return maximum
	}
	return delay
}

func cryptoFloat64() float64 {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0.5
	}
	return float64(binary.BigEndian.Uint64(raw[:])>>11) / float64(uint64(1)<<53)
}

type contextSleeper struct{}

func (contextSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextAttempt(attempt uint64) uint64 {
	if attempt == math.MaxUint64 {
		return attempt
	}
	return attempt + 1
}
