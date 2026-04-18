//go:build e2e

package e2e

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestOutboundText(t *testing.T) {
	resetTelegramMock(t, botToken)
	chatID := nextChatID()
	threadID := ensureChatMapping(t, botToken, chatID)
	resetTelegramMock(t, botToken)

	body := "outbound text " + uuid.NewString()
	sendThreadMessage(t, threadID, body, nil)
	notifyOutbound(t)

	waitForSentMessage(t, botToken, func(message sentMessage) bool {
		return message.Method == "sendMessage" && message.Text == body && message.StatusCode == http.StatusOK
	}, 60*time.Second)
	waitForMessageAck(t, body, 60*time.Second)
}

func TestOutboundMedia(t *testing.T) {
	resetTelegramMock(t, botToken)
	chatID := nextChatID()
	threadID := ensureChatMapping(t, botToken, chatID)
	resetTelegramMock(t, botToken)

	data := []byte("outbound-media-" + uuid.NewString())
	file := uploadFile(t, "photo.png", "image/png", data)
	sendThreadMessage(t, threadID, "", []string{file.GetId()})
	notifyOutbound(t)

	waitForSentMessage(t, botToken, func(message sentMessage) bool {
		if message.Method != "sendPhoto" || message.StatusCode != http.StatusOK {
			return false
		}
		return bytes.Equal(decodeSendData(t, message), data)
	}, 60*time.Second)
}

func TestDeliveryFailures(t *testing.T) {
	t.Run("blocked", func(t *testing.T) {
		resetTelegramMock(t, botToken)
		chatID := nextChatID()
		threadID := ensureChatMapping(t, botToken, chatID)
		resetTelegramMock(t, botToken)

		setSendError(t, botToken, http.StatusForbidden, http.StatusForbidden, "blocked", 0, "sendMessage")
		body := "blocked " + uuid.NewString()
		sendThreadMessage(t, threadID, body, nil)
		notifyOutbound(t)

		waitForSentMessage(t, botToken, func(message sentMessage) bool {
			return message.Method == "sendMessage" && message.Text == body && message.StatusCode == http.StatusForbidden
		}, 60*time.Second)
		waitForMessageAck(t, body, 60*time.Second)

		followUp := "blocked follow-up " + uuid.NewString()
		sendThreadMessage(t, threadID, followUp, nil)
		notifyOutbound(t)
		waitForMessageAck(t, followUp, 60*time.Second)

		messages := getSentMessages(t, botToken)
		for _, message := range messages {
			require.NotEqual(t, followUp, message.Text)
		}
	})

	t.Run("rate_limit", func(t *testing.T) {
		resetTelegramMock(t, botToken)
		chatID := nextChatID()
		threadID := ensureChatMapping(t, botToken, chatID)
		resetTelegramMock(t, botToken)

		setSendError(t, botToken, http.StatusTooManyRequests, http.StatusTooManyRequests, "rate limited", time.Second, "sendMessage")
		body := "rate limited " + uuid.NewString()
		sendThreadMessage(t, threadID, body, nil)
		notifyOutbound(t)
		waitForMessageAck(t, body, 90*time.Second)

		messages := waitForSentMessage(t, botToken, func(message sentMessage) bool {
			return message.Text == body && message.StatusCode == http.StatusOK
		}, 90*time.Second)

		hasRateLimit := false
		hasSuccess := false
		for _, message := range messages {
			if message.Text != body {
				continue
			}
			if message.StatusCode == http.StatusTooManyRequests {
				hasRateLimit = true
			}
			if message.StatusCode == http.StatusOK {
				hasSuccess = true
			}
		}
		require.True(t, hasRateLimit)
		require.True(t, hasSuccess)
	})
}
