//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
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
	installResp, err := appsClient.InstallApp(ctx, &appsv1.InstallAppRequest{
		AppId:          appID,
		OrganizationId: organizationID,
		Configuration:  config,
	})
	cancel()
	require.NoError(t, err)
	secondaryInstallationID := installResp.GetInstallation().GetMeta().GetId()
	require.NotEmpty(t, secondaryInstallationID)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, err := appsClient.UninstallApp(ctx, &appsv1.UninstallAppRequest{Id: secondaryInstallationID})
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
	_, err = appsClient.UninstallApp(ctx, &appsv1.UninstallAppRequest{Id: secondaryInstallationID})
	cancel()
	require.NoError(t, err)

	time.Sleep(70 * time.Second)
	postBody := "post-uninstall " + uuid.NewString()
	enqueueTextUpdate(t, secondaryToken, nextChatID(), postBody)
	assertNoMessage(t, postBody, 20*time.Second)
}
