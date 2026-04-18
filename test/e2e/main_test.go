//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	notificationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/notifications/v1"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
)

var (
	connectorAddress     string
	telegramMockAddress  string
	appsAddress          string
	threadsAddress       string
	filesAddress         string
	notificationsAddress string

	httpClient          *http.Client
	appsClient          appsv1.AppsServiceClient
	threadsClient       threadsv1.ThreadsServiceClient
	filesClient         filesv1.FilesServiceClient
	notificationsClient notificationsv1.NotificationsServiceClient

	organizationID  string
	agentIdentityID string
	appID           string
	appIdentityID   string
	botToken        string
	installationID  string
)

func TestMain(m *testing.M) {
	connectorAddress = envOrDefault("TELEGRAM_CONNECTOR_ADDRESS", "telegram-connector:8080")
	telegramMockAddress = envOrDefault("TELEGRAM_MOCK_ADDRESS", "telegram-mock:8443")
	appsAddress = envOrDefault("APPS_ADDRESS", "apps:50051")
	threadsAddress = envOrDefault("THREADS_ADDRESS", "threads:50051")
	filesAddress = envOrDefault("FILES_ADDRESS", "files:50051")
	notificationsAddress = envOrDefault("NOTIFICATIONS_ADDRESS", "notifications:50051")

	organizationID = requireEnv("TEST_ORGANIZATION_ID")
	agentIdentityID = requireEnv("TEST_AGENT_IDENTITY_ID")

	httpClient = &http.Client{Timeout: 10 * time.Second}

	waitForHealthy(fmt.Sprintf("http://%s/healthz", connectorAddress), 120*time.Second)
	waitForHealthy(fmt.Sprintf("http://%s/_control/healthz", telegramMockAddress), 120*time.Second)

	appsConn, err := grpc.NewClient(appsAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: dial apps: %v\n", err)
		os.Exit(1)
	}
	threadsConn, err := grpc.NewClient(threadsAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: dial threads: %v\n", err)
		os.Exit(1)
	}
	filesConn, err := grpc.NewClient(filesAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: dial files: %v\n", err)
		os.Exit(1)
	}
	notificationsConn, err := grpc.NewClient(notificationsAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: dial notifications: %v\n", err)
		os.Exit(1)
	}

	appsClient = appsv1.NewAppsServiceClient(appsConn)
	threadsClient = threadsv1.NewThreadsServiceClient(threadsConn)
	filesClient = filesv1.NewFilesServiceClient(filesConn)
	notificationsClient = notificationsv1.NewNotificationsServiceClient(notificationsConn)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	appSlug := envOrDefault("TELEGRAM_APP_SLUG", "telegram")
	app, err := resolveApp(ctx, appSlug)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: resolve app: %v\n", err)
		os.Exit(1)
	}
	appID = app.GetMeta().GetId()
	appIdentityID = app.GetIdentityId()
	if appID == "" || appIdentityID == "" {
		fmt.Fprintf(os.Stderr, "e2e setup: app has missing id or identity_id\n")
		os.Exit(1)
	}

	botToken = "e2e-" + uuid.NewString()
	config, err := structpb.NewStruct(map[string]any{
		"bot_token": botToken,
		"agent_id":  agentIdentityID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: build installation config: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	installResp, err := appsClient.InstallApp(ctx, &appsv1.InstallAppRequest{
		AppId:          appID,
		OrganizationId: organizationID,
		Configuration:  config,
	})
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: install app: %v\n", err)
		os.Exit(1)
	}
	installationID = installResp.GetInstallation().GetMeta().GetId()
	if installationID == "" {
		fmt.Fprintf(os.Stderr, "e2e setup: installation missing id\n")
		os.Exit(1)
	}
	if err := resetTelegramMockToken(botToken); err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: reset telegram mock: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Second)
	_, err = appsClient.UninstallApp(ctx, &appsv1.UninstallAppRequest{Id: installationID})
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e cleanup: uninstall app: %v\n", err)
	}

	if err := appsConn.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e cleanup: close apps conn: %v\n", err)
	}
	if err := threadsConn.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e cleanup: close threads conn: %v\n", err)
	}
	if err := filesConn.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e cleanup: close files conn: %v\n", err)
	}
	if err := notificationsConn.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e cleanup: close notifications conn: %v\n", err)
	}

	os.Exit(code)
}
