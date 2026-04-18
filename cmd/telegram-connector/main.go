package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openziti/sdk-golang/ziti"

	"github.com/agynio/telegram-connector/internal/config"
	"github.com/agynio/telegram-connector/internal/connector"
	"github.com/agynio/telegram-connector/internal/db"
	"github.com/agynio/telegram-connector/internal/enrollment"
	"github.com/agynio/telegram-connector/internal/gateway"
	"github.com/agynio/telegram-connector/internal/store"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		log.Fatalf("telegram-connector: %v", err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("parse database url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("create connection pool: %w", err)
	}
	defer pool.Close()

	if err := db.ApplyMigrations(ctx, pool); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	enrolled, err := enrollment.Enroll(ctx, cfg.GatewayURL, cfg.ServiceToken)
	if err != nil {
		return fmt.Errorf("enroll app: %w", err)
	}
	if err := os.WriteFile(cfg.ZitiIdentityFile, enrolled.IdentityJSON, 0600); err != nil {
		return fmt.Errorf("write ziti identity: %w", err)
	}

	zitiCfg, err := ziti.NewConfigFromFile(cfg.ZitiIdentityFile)
	if err != nil {
		return fmt.Errorf("load ziti identity: %w", err)
	}
	zitiCtx, err := ziti.NewContext(zitiCfg)
	if err != nil {
		return fmt.Errorf("create ziti context: %w", err)
	}

	store := store.NewStore(pool)
	gatewayClient := gateway.NewClient(
		gateway.NewZitiHTTPClient(zitiCtx),
		fmt.Sprintf("http://%s", cfg.GatewayServiceName),
		enrolled.IdentityID,
	)

	service := connector.NewService(store, gatewayClient, connector.ServiceConfig{
		AppID:           cfg.AppID,
		TelegramBaseURL: cfg.TelegramBaseURL,
		PollTimeout:     cfg.TelegramPollTimeout,
	})

	serviceErrCh := make(chan error, 1)
	go func() {
		if err := service.Run(ctx); err != nil {
			serviceErrCh <- err
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	zitiListener, err := zitiCtx.ListenWithOptions(cfg.ZitiServiceName, &ziti.ListenOptions{})
	if err != nil {
		return fmt.Errorf("listen on ziti service %s: %w", cfg.ZitiServiceName, err)
	}
	tcpListener, err := net.Listen("tcp", cfg.HTTPAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddress, err)
	}

	zitiServer := &http.Server{Handler: mux}
	tcpServer := &http.Server{Handler: mux}
	errCh := make(chan error, 2)

	go func() {
		if err := zitiServer.Serve(zitiListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("serve ziti listener: %w", err)
		}
	}()
	go func() {
		if err := tcpServer.Serve(tcpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("serve tcp listener: %w", err)
		}
	}()

	log.Printf("telegram-connector listening on %s (ziti service)", cfg.ZitiServiceName)
	log.Printf("health endpoints listening on %s", cfg.HTTPAddress)

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errCh:
	case serveErr = <-serviceErrCh:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	zitiErr := zitiServer.Shutdown(shutdownCtx)
	if zitiErr != nil {
		zitiErr = fmt.Errorf("shutdown ziti server: %w", zitiErr)
	}
	tcpErr := tcpServer.Shutdown(shutdownCtx)
	if tcpErr != nil {
		tcpErr = fmt.Errorf("shutdown tcp server: %w", tcpErr)
	}
	shutdownErr := errors.Join(zitiErr, tcpErr)
	if shutdownErr != nil {
		return shutdownErr
	}
	return serveErr
}
