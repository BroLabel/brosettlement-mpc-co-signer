package preparams

import (
	"errors"

	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

// ProductionProfile returns the fixed co-signer pool policy. The caller
// supplies only the explicitly benchmarked CPU parallelism bound.
func ProductionProfile(generationParallelism int) (coretss.PreParamsConfig, error) {
	if generationParallelism < 1 {
		return coretss.PreParamsConfig{}, errors.New("preparams generation parallelism must be >= 1")
	}
	profile := coretss.DefaultPreParamsConfig()
	profile.TargetSize = 2
	profile.MaxConcurrency = 1
	profile.GenerationParallelism = generationParallelism
	profile.SyncFallbackOnEmpty = false
	profile.AutoRefillOnAcquire = false
	return profile, nil
}
