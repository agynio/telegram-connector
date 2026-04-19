//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	"github.com/agynio/telegram-connector/test/e2e/bootstrap"
)

func TestReconciliation(t *testing.T) {
	secondaryToken := "reconcile-" + uuid.NewString()
	resetTelegramMock(t, secondaryToken)

	config, err := structpb.NewStruct(map[string]any{
		"bot_token": secondaryToken,
		"agent_id":  agentIdentityID,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	installResp, err := appsClient.InstallApp(bootstrap.IdentityContext(ctx, ownerIdentityID), &appsv1.InstallAppRequest{
		AppId:          appID,
		OrganizationId: organizationID,
		Configuration:  config,
	})
	cancel()
	secondaryInstallationID := ""
	usedExistingInstallation := false
	if err != nil {
		if status.Code(err) != codes.AlreadyExists {
			require.NoError(t, err)
		}
		secondaryInstallationID = installationID
		usedExistingInstallation = true
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		_, err = appsClient.UpdateInstallation(bootstrap.IdentityContext(ctx, ownerIdentityID), &appsv1.UpdateInstallationRequest{
			Id:            secondaryInstallationID,
			Configuration: config,
		})
		cancel()
		require.NoError(t, err)
	} else {
		secondaryInstallationID = installResp.GetInstallation().GetMeta().GetId()
		require.NotEmpty(t, secondaryInstallationID)
	}

	originalConfig, err := structpb.NewStruct(map[string]any{
		"bot_token": botToken,
		"agent_id":  agentIdentityID,
	})
	require.NoError(t, err)

	uninstalled := false
	t.Cleanup(func() {
		if usedExistingInstallation {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if uninstalled {
				installResp, err := appsClient.InstallApp(bootstrap.IdentityContext(ctx, ownerIdentityID), &appsv1.InstallAppRequest{
					AppId:          appID,
					OrganizationId: organizationID,
					Configuration:  originalConfig,
				})
				if err != nil {
					t.Logf("cleanup reinstall failed: %v", err)
				} else if installResp.GetInstallation() != nil && installResp.GetInstallation().GetMeta().GetId() != "" {
					installationID = installResp.GetInstallation().GetMeta().GetId()
				}
			} else {
				_, err := appsClient.UpdateInstallation(bootstrap.IdentityContext(ctx, ownerIdentityID), &appsv1.UpdateInstallationRequest{
					Id:            secondaryInstallationID,
					Configuration: originalConfig,
				})
				if err != nil {
					t.Logf("cleanup restore config failed: %v", err)
				}
			}
			cancel()
			if err := resetTelegramMockToken(botToken); err != nil {
				t.Logf("cleanup reset telegram mock failed: %v", err)
			}
			return
		}

		if secondaryInstallationID == "" || uninstalled {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, err := appsClient.UninstallApp(bootstrap.IdentityContext(ctx, ownerIdentityID), &appsv1.UninstallAppRequest{Id: secondaryInstallationID})
		cancel()
		if err != nil {
			t.Logf("cleanup uninstall failed: %v", err)
		}
	})

	chatID := nextChatID()
	body := "reconcile inbound " + uuid.NewString()
	enqueueTextUpdate(t, secondaryToken, chatID, body)
	waitForMessage(t, body, 0, 90*time.Second)

	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Second)
	_, err = appsClient.UninstallApp(bootstrap.IdentityContext(ctx, ownerIdentityID), &appsv1.UninstallAppRequest{Id: secondaryInstallationID})
	cancel()
	require.NoError(t, err)
	uninstalled = true

	time.Sleep(70 * time.Second)
	postBody := "post-uninstall " + uuid.NewString()
	enqueueTextUpdate(t, secondaryToken, nextChatID(), postBody)
	assertNoMessage(t, postBody, 20*time.Second)
}
