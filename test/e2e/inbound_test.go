//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestInboundText(t *testing.T) {
	resetTelegramMock(t, botToken)
	chatID := nextChatID()
	body := "inbound text " + uuid.NewString()

	enqueueTextUpdate(t, botToken, chatID, body)
	_, message := waitForMessage(t, body, 0, 90*time.Second)

	require.Equal(t, body, message.GetBody())
	require.Empty(t, message.GetFileIds())
	require.Equal(t, appIdentityID, message.GetSenderId())
}

func TestInboundMedia(t *testing.T) {
	resetTelegramMock(t, botToken)
	chatID := nextChatID()
	caption := "inbound media " + uuid.NewString()
	fileID := "photo-" + uuid.NewString()
	filePath := "photos/" + fileID + ".jpg"
	data := []byte("photo-data-" + uuid.NewString())

	setTelegramFile(t, botToken, fileID, filePath, "application/octet-stream", data)
	enqueuePhotoUpdate(t, botToken, chatID, fileID, caption, int64(len(data)))
	_, message := waitForMessage(t, caption, 1, 90*time.Second)

	require.Len(t, message.GetFileIds(), 1)
	uploadedID := message.GetFileIds()[0]
	metadata := getFileMetadata(t, uploadedID)
	require.Equal(t, int64(len(data)), metadata.GetSizeBytes())
	require.Equal(t, "image/jpeg", metadata.GetContentType())
	downloaded := downloadFile(t, uploadedID)
	require.Equal(t, data, downloaded)
}
