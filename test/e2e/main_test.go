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
	organizationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/organizations/v1"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
	"github.com/agynio/telegram-connector/test/e2e/bootstrap"
)

var (
	connectorAddress     string
	telegramMockAddress  string
	appsAddress          string
	organizationsAddress string
	threadsAddress       string
	filesAddress         string
	notificationsAddress string
	localEnv             *localEnvironment

	httpClient          *http.Client
	appsClient          appsv1.AppsServiceClient
	organizationsClient organizationsv1.OrganizationsServiceClient
	threadsClient       threadsv1.ThreadsServiceClient
	filesClient         filesv1.FilesServiceClient
	notificationsClient notificationsv1.NotificationsServiceClient

	organizationID   string
	ownerIdentityID  string
	agentIdentityID  string
	appID            string
	appIdentityID    string
	botToken         string
	installationID   string
	cleanupResources bool
)

func TestMain(m *testing.M) {
	useExternal := os.Getenv("E2E_EXTERNAL") == "true"
	if useExternal {
		connectorAddress = envOrDefault("TELEGRAM_CONNECTOR_ADDRESS", "telegram-connector:8080")
		telegramMockAddress = envOrDefault("TELEGRAM_MOCK_ADDRESS", "telegram-mock:8443")
		appsAddress = envOrDefault("APPS_ADDRESS", "apps:50051")
		organizationsAddress = envOrDefault("ORGANIZATIONS_ADDRESS", "tenants:50051")
		threadsAddress = envOrDefault("THREADS_ADDRESS", "threads:50051")
		filesAddress = envOrDefault("FILES_ADDRESS", "files:50051")
		notificationsAddress = envOrDefault("NOTIFICATIONS_ADDRESS", "notifications:50051")
	} else {
		env, err := startLocalEnvironment()
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e setup: local env: %v\n", err)
			os.Exit(1)
		}
		localEnv = env
		connectorAddress = env.connectorAddr
		telegramMockAddress = env.telegramAddr
		appsAddress = env.appsAddr
		organizationsAddress = env.organizationsAddr
		threadsAddress = env.threadsAddr
		filesAddress = env.filesAddr
		notificationsAddress = env.notificationsAddr
	}

	organizationID = os.Getenv("E2E_ORGANIZATION_ID")
	appID = os.Getenv("E2E_APP_ID")
	appIdentityID = os.Getenv("E2E_APP_IDENTITY_ID")
	ownerIdentityID = os.Getenv("E2E_OWNER_IDENTITY_ID")
	agentIdentityID = os.Getenv("E2E_AGENT_IDENTITY_ID")
	cleanupResources = os.Getenv("E2E_CLEANUP") == "true"

	hasBootstrapEnv := organizationID != "" || appID != "" || appIdentityID != "" || ownerIdentityID != ""
	if hasBootstrapEnv {
		if organizationID == "" || appID == "" || appIdentityID == "" || ownerIdentityID == "" {
			fmt.Fprintln(os.Stderr, "e2e setup: E2E_* env incomplete (need ORG, APP, APP_IDENTITY, OWNER)")
			os.Exit(1)
		}
		if agentIdentityID == "" {
			agentIdentityID = ownerIdentityID
		}
	} else {
		ownerIdentityID = uuid.NewString()
		agentIdentityID = ownerIdentityID
	}

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
	organizationsConn, err := grpc.NewClient(organizationsAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: dial organizations: %v\n", err)
		os.Exit(1)
	}

	appsClient = appsv1.NewAppsServiceClient(appsConn)
	organizationsClient = organizationsv1.NewOrganizationsServiceClient(organizationsConn)
	threadsClient = threadsv1.NewThreadsServiceClient(threadsConn)
	filesClient = filesv1.NewFilesServiceClient(filesConn)
	notificationsClient = notificationsv1.NewNotificationsServiceClient(notificationsConn)

	if !hasBootstrapEnv {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result, err := bootstrap.Create(ctx, organizationsClient, appsClient, ownerIdentityID)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e setup: bootstrap: %v\n", err)
			os.Exit(1)
		}
		organizationID = result.OrganizationID
		appID = result.AppID
		appIdentityID = result.AppIdentityID
		cleanupResources = true
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	installResp, err := appsClient.InstallApp(bootstrap.IdentityContext(ctx, ownerIdentityID), &appsv1.InstallAppRequest{
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
	if localEnv != nil {
		localEnv.StartConnector(appID, appIdentityID)
	}

	code := m.Run()

	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Second)
	_, err = appsClient.UninstallApp(bootstrap.IdentityContext(ctx, ownerIdentityID), &appsv1.UninstallAppRequest{Id: installationID})
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e cleanup: uninstall app: %v\n", err)
	}
	if cleanupResources {
		ctx, cancel = context.WithTimeout(context.Background(), 20*time.Second)
		_, err = appsClient.DeleteApp(bootstrap.IdentityContext(ctx, ownerIdentityID), &appsv1.DeleteAppRequest{Id: appID})
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e cleanup: delete app: %v\n", err)
		}

		ctx, cancel = context.WithTimeout(context.Background(), 20*time.Second)
		_, err = organizationsClient.DeleteOrganization(bootstrap.IdentityContext(ctx, ownerIdentityID), &organizationsv1.DeleteOrganizationRequest{Id: organizationID})
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e cleanup: delete organization: %v\n", err)
		}
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
	if err := organizationsConn.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e cleanup: close organizations conn: %v\n", err)
	}
	if localEnv != nil {
		localEnv.Shutdown()
	}

	os.Exit(code)
}
