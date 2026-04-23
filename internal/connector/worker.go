package connector

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	"github.com/agynio/telegram-connector/internal/telegram"
)

type installationWorker struct {
	installation        Installation
	store               Store
	gateway             Gateway
	telegramClient      *telegram.Client
	pollTimeout         time.Duration
	cancel              context.CancelFunc
	done                chan struct{}
	status              *installationStatus
	telegramFailureAt   time.Time
	telegramUnreachable bool
}

type stopReason int

const (
	stopReasonShutdown stopReason = iota
	stopReasonRemoved
)

func newInstallationWorker(installation Installation, store Store, gatewayClient Gateway, telegramBaseURL string, pollTimeout time.Duration) *installationWorker {
	return &installationWorker{
		installation:   installation,
		store:          store,
		gateway:        gatewayClient,
		telegramClient: telegram.NewClient(installation.BotToken, telegramBaseURL, nil),
		pollTimeout:    pollTimeout,
		done:           make(chan struct{}),
	}
}

func (w *installationWorker) Installation() Installation {
	return w.installation
}

func (w *installationWorker) Start(parent context.Context) {
	if w.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	go w.run(ctx)
}

func (w *installationWorker) Stop(reason stopReason) {
	if w.cancel == nil {
		return
	}
	w.reportStop(reason)
	w.cancel()
	<-w.done
}

func (w *installationWorker) run(ctx context.Context) {
	defer close(w.done)
	state, err := w.loadInstallationState(ctx)
	if err != nil {
		log.Printf("connector: load installation state %s error: %v", w.installation.ID, err)
		return
	}
	startAt := time.Now().UTC()
	w.status = newInstallationStatus(w.installation.ID, w.store, w.gateway, startAt, state)
	w.appendAudit(ctx, auditEvent{
		name:           auditEventPollingStarted,
		message:        fmt.Sprintf("%s: polling started", auditEventPollingStarted),
		level:          appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_INFO,
		idempotencyKey: auditKeyWithTime(auditEventPollingStarted, w.installation.ID, startAt),
	})
	w.status.Report(ctx)

	statusCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go w.runStatusReporter(statusCtx)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := w.runInbound(ctx, state); err != nil {
			log.Printf("connector: inbound worker %s stopped: %v", w.installation.ID, err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := w.runOutbound(ctx); err != nil {
			log.Printf("connector: outbound worker %s stopped: %v", w.installation.ID, err)
		}
	}()
	wg.Wait()
}

func (w *installationWorker) reportStop(reason stopReason) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w.appendAudit(ctx, auditEvent{
		name:           auditEventPollingStopped,
		message:        fmt.Sprintf("%s: polling stopped", auditEventPollingStopped),
		level:          appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_INFO,
		idempotencyKey: auditKeyWithTime(auditEventPollingStopped, w.installation.ID, time.Now().UTC()),
	})
	if reason == stopReasonRemoved {
		if err := w.gateway.ReportInstallationStatus(ctx, w.installation.ID.String(), ""); err != nil {
			log.Printf("connector: clear status error: %v", err)
			if w.status != nil {
				w.status.RecordError(time.Now().UTC(), fmt.Sprintf("clear status: %v", err))
			}
		}
		return
	}
	if w.status != nil {
		w.status.SetStopped(true)
		w.status.Report(ctx)
	}
}

func (w *installationWorker) runStatusReporter(ctx context.Context) {
	ticker := time.NewTicker(statusReportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.status != nil {
				w.status.Report(ctx)
			}
		}
	}
}
