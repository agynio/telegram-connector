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
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openziti/sdk-golang/ziti"

	"github.com/agynio/telegram-connector/internal/config"
	"github.com/agynio/telegram-connector/internal/connector"
	"github.com/agynio/telegram-connector/internal/db"
	"github.com/agynio/telegram-connector/internal/enrollment"
	"github.com/agynio/telegram-connector/internal/gateway"
	"github.com/agynio/telegram-connector/internal/store"
)

const (
	shutdownTimeout       = 10 * time.Second
	startupTimeout        = 90 * time.Second
	zitiListenRetryDelay  = 2 * time.Second
	gatewayRetryDelay     = 2 * time.Second
	gatewayAttemptTimeout = 10 * time.Second
)

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
	startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()

	store := store.NewStore(pool)
	gatewayClient := gateway.NewClient(
		gateway.NewZitiHTTPClient(zitiCtx),
		fmt.Sprintf("http://%s", cfg.GatewayServiceName),
		enrolled.IdentityID,
	)
	if err := waitForGatewayReady(startupCtx, gatewayClient, cfg.AppID); err != nil {
		return fmt.Errorf("wait for gateway: %w", err)
	}

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

	zitiListener, err := listenZitiWithRetry(startupCtx, zitiCtx, cfg.ZitiServiceName)
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

func listenZitiWithRetry(ctx context.Context, zitiCtx ziti.Context, serviceName string) (net.Listener, error) {
	for {
		listener, err := zitiCtx.ListenWithOptions(serviceName, &ziti.ListenOptions{})
		if err == nil {
			return listener, nil
		}
		if !isIdentityMissingError(err) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, err
		}
		log.Printf("ziti listener not ready, retrying: %v", err)
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(zitiListenRetryDelay):
		}
	}
}

func waitForGatewayReady(ctx context.Context, client *gateway.Client, appID string) error {
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, gatewayAttemptTimeout)
		_, err := client.ListInstallations(attemptCtx, appID)
		cancel()
		if err == nil {
			return nil
		}
		if !isGatewayRetryableError(err) {
			return err
		}
		if ctx.Err() != nil {
			return err
		}
		log.Printf("gateway not ready, retrying: %v", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(gatewayRetryDelay):
		}
	}
}

func isGatewayRetryableError(err error) bool {
	if isIdentityMissingError(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr.Code() == connect.CodeUnavailable
	}
	return false
}

func isIdentityMissingError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "identity not found")
}
