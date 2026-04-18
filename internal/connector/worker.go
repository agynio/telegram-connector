package connector

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/agynio/telegram-connector/internal/gateway"
	"github.com/agynio/telegram-connector/internal/telegram"
)

type installationWorker struct {
	installation   Installation
	store          Store
	gateway        *gateway.Client
	telegramClient *telegram.Client
	pollTimeout    time.Duration
	cancel         context.CancelFunc
	done           chan struct{}
}

func newInstallationWorker(installation Installation, store Store, gatewayClient *gateway.Client, telegramBaseURL string, pollTimeout time.Duration) *installationWorker {
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

func (w *installationWorker) Stop() {
	if w.cancel == nil {
		return
	}
	w.cancel()
	<-w.done
}

func (w *installationWorker) run(ctx context.Context) {
	defer close(w.done)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := w.runInbound(ctx); err != nil {
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
