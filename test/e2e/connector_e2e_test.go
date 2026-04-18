//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	"github.com/agynio/telegram-connector/.gen/go/agynio/api/gateway/v1/gatewayv1connect"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
	"github.com/agynio/telegram-connector/internal/connector"
	"github.com/agynio/telegram-connector/internal/gateway"
	"github.com/agynio/telegram-connector/internal/telegram"
)

func TestConnectorInboundOutbound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	appID := "app-1"
	installationID := uuid.New()
	organizationID := uuid.New()
	agentID := uuid.New()
	botToken := "test-token"

	cfg, err := structpb.NewStruct(map[string]any{
		"bot_token": botToken,
		"agent_id":  agentID.String(),
	})
	require.NoError(t, err)

	installation := &appsv1.Installation{
		Meta:           &appsv1.EntityMeta{Id: installationID.String()},
		AppId:          appID,
		OrganizationId: organizationID.String(),
		Configuration:  cfg,
	}

	mockGateway := newMockGateway([]*appsv1.Installation{installation})
	mux := http.NewServeMux()
	path, handler := gatewayv1connect.NewAppsGatewayHandler(mockGateway)
	mux.Handle(path, handler)
	path, handler = gatewayv1connect.NewThreadsGatewayHandler(mockGateway)
	mux.Handle(path, handler)
	path, handler = gatewayv1connect.NewFilesGatewayHandler(mockGateway)
	mux.Handle(path, handler)
	path, handler = gatewayv1connect.NewNotificationsGatewayHandler(mockGateway)
	mux.Handle(path, handler)

	gatewayServer := httptest.NewServer(mux)

	mockTelegram := newMockTelegram(botToken)
	telegramServer := httptest.NewServer(mockTelegram)

	store := newMockStore()
	gatewayClient := gateway.NewClient(http.DefaultClient, gatewayServer.URL, uuid.New().String())
	service := connector.NewService(store, gatewayClient, connector.ServiceConfig{
		AppID:           appID,
		TelegramBaseURL: telegramServer.URL,
		PollTimeout:     time.Second,
	})

	go func() {
		_ = service.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		telegramServer.CloseClientConnections()
		gatewayServer.CloseClientConnections()
		telegramServer.Close()
		gatewayServer.Close()
	})

	photoData := []byte("photo-data")
	mockTelegram.AddFile("tg-photo-1", "photos/photo.jpg", photoData, "image/jpeg")
	mockTelegram.QueueUpdate(telegram.Update{
		UpdateID: 1,
		Message: &telegram.Message{
			MessageID: 10,
			From:      &telegram.User{ID: 55},
			Chat:      telegram.Chat{ID: 100, Type: "private"},
			Photo:     []telegram.Photo{{FileID: "tg-photo-1", FileSize: int64(len(photoData))}},
			Caption:   "photo caption",
		},
	})

	require.Eventually(t, func() bool {
		messages := mockGateway.SentMessages()
		if len(messages) == 0 {
			return false
		}
		if messages[0].Body != "photo caption" || len(messages[0].FileIds) != 1 {
			return false
		}
		for _, record := range mockGateway.UploadedFiles() {
			if bytes.Equal(record.data, photoData) {
				return true
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond)

	mapping, ok := store.getMappingByChat(installationID, 100)
	require.True(t, ok)

	inlineData := []byte("inline-image")
	inlineID := "inline-file"
	mockGateway.AddFileWithID(inlineID, "inline.png", "image/png", inlineData)
	docData := []byte("doc-data")
	docID := "doc-file"
	mockGateway.AddFileWithID(docID, "doc.pdf", "application/pdf", docData)

	outboundMessage := &threadsv1.Message{
		Id:       uuid.New().String(),
		ThreadId: mapping.ThreadID.String(),
		SenderId: uuid.New().String(),
		Body:     "hello ![img](agyn://file/" + inlineID + ")",
		FileIds:  []string{docID},
	}
	mockGateway.QueueUnackedMessage(outboundMessage)
	mockGateway.notifications.Notify()

	require.Eventually(t, func() bool {
		sends := mockTelegram.Sends()
		var messageSent, photoSent, docSent bool
		for _, send := range sends {
			switch send.method {
			case "sendMessage":
				if send.text == "hello" {
					messageSent = true
				}
			case "sendPhoto":
				if bytes.Equal(send.data, inlineData) {
					photoSent = true
				}
			case "sendDocument":
				if bytes.Equal(send.data, docData) {
					docSent = true
				}
			}
		}
		return messageSent && photoSent && docSent && mockGateway.UnackedCount() == 0
	}, 5*time.Second, 100*time.Millisecond)
}
