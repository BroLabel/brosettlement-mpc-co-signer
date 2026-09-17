package main

import (
	"context"
	"crypto/ed25519"
	"io"
	"log/slog"
	"net"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/lifecycle"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/reconcile"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/terminal"
)

func newLifecycleCoordinator(log *slog.Logger, cfg config.Config, privateKey ed25519.PrivateKey) (*lifecycle.Coordinator, error) {
	readiness := health.NewReadiness()
	var resources *applicationResources
	var healthListener net.Listener
	return lifecycle.NewCoordinator(lifecycle.Dependencies{
		AcquireLock: func() (io.Closer, error) {
			return lifecycle.AcquireLifetimeLock(cfg.LockPath)
		},
		OpenCapabilities: func(openCtx context.Context) (io.Closer, error) {
			opened, openErr := openApplicationResources(openCtx, log, cfg, privateKey, readiness)
			if openErr != nil {
				return nil, openErr
			}
			resources = opened
			return opened, nil
		},
		StartPublisher: func(publisherCtx context.Context) error {
			return resources.startupPublisher.Start(publisherCtx)
		},
		Reconcile: func(reconcileCtx context.Context) (reconcile.Result, error) {
			return resources.reconciler.Reconcile(reconcileCtx)
		},
		Handoff: func(handoffCtx context.Context, job terminal.Job, done func(terminal.PublishResult)) error {
			return resources.startupPublisher.Handoff(handoffCtx, job, done)
		},
		SetDKGAdmissionOpen: func(open bool) {
			if resources != nil {
				resources.scheduler.SetDKGAdmissionOpen(open)
			}
		},
		ProvisioningReady: func() bool {
			return resources.provisioningReady()
		},
		SigningReady: func() bool { return resources != nil && resources.signingReady() },
		StartScheduler: func(schedulerCtx context.Context) {
			resources.startScheduler(schedulerCtx)
		},
		WakeScheduler: func() {
			resources.scheduler.Wake()
		},
		StartIntake: func(context.Context) error {
			listener, listenErr := net.Listen("tcp", resources.healthServer.Addr)
			if listenErr != nil {
				return listenErr
			}
			healthListener = listener
			go serveHealthListener(log, resources.healthServer, listener)
			return nil
		},
		StopIntake: func(stopCtx context.Context) error {
			if healthListener == nil {
				return nil
			}
			return resources.healthServer.Shutdown(stopCtx)
		},
		Drain: func(drainCtx context.Context) error {
			if resources == nil {
				return nil
			}
			return drainWorkers(drainCtx, resources.scheduler.Semaphore())
		},
		ReportDrainError: func(err error) {
			log.Warn("worker drain failed; shutdown is waiting for owned work to stop", "err", err)
		},
		WaitPublisher: func() {
			if resources != nil {
				resources.startupPublisher.Wait()
			}
		},
		Readiness: readiness,
	})
}
