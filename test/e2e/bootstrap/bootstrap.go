//go:build e2e

package bootstrap

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	organizationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/organizations/v1"
)

type Result struct {
	OrganizationID  string
	OwnerIdentityID string
	AppID           string
	AppIdentityID   string
	AppSlug         string
	ServiceToken    string
}

func IdentityContext(ctx context.Context, identityID string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("x-identity-id", identityID))
}

func Create(ctx context.Context, organizationsClient organizationsv1.OrganizationsServiceClient, appsClient appsv1.AppsServiceClient, ownerIdentityID string) (Result, error) {
	identityCtx := IdentityContext(ctx, ownerIdentityID)

	orgName := fmt.Sprintf("Telegram E2E %s", uuid.NewString())
	orgResp, err := organizationsClient.CreateOrganization(identityCtx, &organizationsv1.CreateOrganizationRequest{Name: orgName})
	if err != nil {
		return Result{}, fmt.Errorf("create organization: %w", err)
	}
	organizationID := orgResp.GetOrganization().GetId()
	if organizationID == "" {
		return Result{}, fmt.Errorf("organization missing id")
	}

	slugSuffix := uuid.NewString()
	appSlug := fmt.Sprintf("telegram-e2e-%s", slugSuffix)
	appName := fmt.Sprintf("Telegram E2E %s", slugSuffix)
	appResp, err := appsClient.CreateApp(identityCtx, &appsv1.CreateAppRequest{
		OrganizationId: organizationID,
		Slug:           appSlug,
		Name:           appName,
		Description:    "Telegram connector E2E",
		Visibility:     appsv1.AppVisibility_APP_VISIBILITY_INTERNAL,
		Permissions: []string{
			"thread:create",
			"thread:write",
			"participant:add",
		},
	})
	if err != nil {
		return Result{}, fmt.Errorf("create app: %w", err)
	}
	app := appResp.GetApp()
	if app == nil || app.GetMeta() == nil {
		return Result{}, fmt.Errorf("app missing metadata")
	}
	appID := app.GetMeta().GetId()
	appIdentityID := app.GetIdentityId()
	if appID == "" || appIdentityID == "" {
		return Result{}, fmt.Errorf("app missing id or identity_id")
	}

	return Result{
		OrganizationID:  organizationID,
		OwnerIdentityID: ownerIdentityID,
		AppID:           appID,
		AppIdentityID:   appIdentityID,
		AppSlug:         appSlug,
		ServiceToken:    appResp.GetServiceToken(),
	}, nil
}
