package worker

import "sync"

type permitPool struct {
	slots chan struct{}
}

func newPermitPool(capacity int) *permitPool {
	if capacity < 1 {
		capacity = 1
	}
	return &permitPool{slots: make(chan struct{}, capacity)}
}

func (p *permitPool) tryAcquire() *permitToken {
	if p == nil {
		return nil
	}
	select {
	case p.slots <- struct{}{}:
		return &permitToken{owner: p}
	default:
		return nil
	}
}

type permitToken struct {
	owner *permitPool
	once  sync.Once
}

func (p *permitToken) release() {
	if p == nil || p.owner == nil {
		return
	}
	p.once.Do(func() {
		<-p.owner.slots
	})
}

type generalPermitToken struct {
	token *permitToken
}

func (p *generalPermitToken) release() {
	if p != nil {
		p.token.release()
	}
}

type dkgGuardPermitToken struct {
	token *permitToken
}

func (p *dkgGuardPermitToken) release() {
	if p != nil {
		p.token.release()
	}
}

type schedulerPermits struct {
	general *permitPool
	dkg     *permitPool
	wakeups chan<- struct{}
}

func newSchedulerPermits(maxConcurrent int, wakeups chan<- struct{}) *schedulerPermits {
	return &schedulerPermits{
		general: newPermitPool(maxConcurrent),
		dkg:     newPermitPool(1),
		wakeups: wakeups,
	}
}

func (p *schedulerPermits) tryAcquireSIGN() *jobPermitLease {
	if p == nil {
		return nil
	}
	general := p.general.tryAcquire()
	if general == nil {
		return nil
	}
	return &jobPermitLease{
		general: &generalPermitToken{token: general},
		wakeups: p.wakeups,
	}
}

func (p *schedulerPermits) tryAcquireDKG() *jobPermitLease {
	if p == nil {
		return nil
	}
	guard := p.dkg.tryAcquire()
	if guard == nil {
		return nil
	}
	general := p.general.tryAcquire()
	if general == nil {
		guard.release()
		return nil
	}
	return &jobPermitLease{
		general: &generalPermitToken{token: general},
		dkg:     &dkgGuardPermitToken{token: guard},
		wakeups: p.wakeups,
	}
}

// jobPermitLease is the narrow ownership boundary carried by one claimed
// scheduler job. A normal DKG owns both typed tokens until the terminal owner
// releases the lease; SIGN owns only the general token.
type jobPermitLease struct {
	general *generalPermitToken
	dkg     *dkgGuardPermitToken
	wakeups chan<- struct{}
	once    sync.Once
}

func (p *jobPermitLease) Release() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.general.release()
		p.dkg.release()
		if p.wakeups != nil {
			select {
			case p.wakeups <- struct{}{}:
			default:
			}
		}
	})
}

func newLegacyGeneralLease(sem chan struct{}, wakeups chan<- struct{}) *jobPermitLease {
	if sem == nil {
		return &jobPermitLease{wakeups: wakeups}
	}
	return &jobPermitLease{
		general: &generalPermitToken{token: &permitToken{owner: &permitPool{slots: sem}}},
		wakeups: wakeups,
	}
}
