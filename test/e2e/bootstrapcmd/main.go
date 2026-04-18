//go:build e2e

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	organizationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/organizations/v1"
	"github.com/agynio/telegram-connector/test/e2e/bootstrap"
)

const bootstrapTimeout = 30 * time.Second

func main() {
	appsAddress := envOrDefault("APPS_ADDRESS", "apps:50051")
	organizationsAddress := envOrDefault("ORGANIZATIONS_ADDRESS", "tenants:50051")
	ownerIdentityID := envOrDefault("E2E_OWNER_IDENTITY_ID", uuid.NewString())

	appsConn, err := grpc.NewClient(appsAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: dial apps: %v\n", err)
		os.Exit(1)
	}
	organizationsConn, err := grpc.NewClient(organizationsAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: dial organizations: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		_ = appsConn.Close()
		_ = organizationsConn.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), bootstrapTimeout)
	defer cancel()
	result, err := bootstrap.Create(ctx, organizationsv1.NewOrganizationsServiceClient(organizationsConn), appsv1.NewAppsServiceClient(appsConn), ownerIdentityID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("E2E_OWNER_IDENTITY_ID=%s\n", result.OwnerIdentityID)
	fmt.Printf("E2E_AGENT_IDENTITY_ID=%s\n", result.OwnerIdentityID)
	fmt.Printf("E2E_ORGANIZATION_ID=%s\n", result.OrganizationID)
	fmt.Printf("E2E_APP_ID=%s\n", result.AppID)
	fmt.Printf("E2E_APP_IDENTITY_ID=%s\n", result.AppIdentityID)
	fmt.Printf("E2E_APP_SLUG=%s\n", result.AppSlug)
	fmt.Printf("E2E_ZITI_SERVICE_NAME=app-%s\n", result.AppSlug)
	fmt.Printf("E2E_SERVICE_TOKEN=%s\n", result.ServiceToken)
	fmt.Printf("E2E_CLEANUP=true\n")
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
