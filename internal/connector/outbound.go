package connector

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
	"github.com/agynio/telegram-connector/internal/store"
	"github.com/agynio/telegram-connector/internal/telegram"
)

const (
	outboundPollInterval = 30 * time.Second
	outboundRetryBase    = time.Second
	outboundRetryCap     = 32 * time.Second
	outboundRetryMax     = 3
)

func (w *installationWorker) runOutbound(ctx context.Context) error {
	trigger := make(chan struct{}, 1)
	go w.subscribeNotifications(ctx, trigger)

	ticker := time.NewTicker(outboundPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			notify(trigger)
		case <-trigger:
			w.processOutbound(ctx)
		}
	}
}

func (w *installationWorker) subscribeNotifications(ctx context.Context, trigger chan<- struct{}) {
	backoff := outboundRetryBase
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		stream, err := w.gateway.Subscribe(ctx)
		if err != nil {
			log.Printf("connector: subscribe notifications error: %v", err)
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = outboundRetryBase
		for stream.Receive() {
			notify(trigger)
		}
		if err := stream.Err(); err != nil {
			log.Printf("connector: notification stream error: %v", err)
		}
		if !sleepContext(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

func (w *installationWorker) processOutbound(ctx context.Context) {
	if w.status != nil && w.status.TokenRejected() {
		return
	}
	messages, err := w.gateway.GetUnackedMessages(ctx)
	if err != nil {
		log.Printf("connector: get unacked messages error: %v", err)
		return
	}
	for _, message := range messages {
		if w.status != nil && w.status.TokenRejected() {
			return
		}
		shouldAck := false
		if message.GetSenderId() == w.gateway.AppIdentityID() {
			shouldAck = true
		}
		if !shouldAck {
			threadID, err := uuid.Parse(message.GetThreadId())
			if err != nil {
				log.Printf("connector: invalid thread id %s: %v", message.GetThreadId(), err)
				shouldAck = true
			} else {
				mapping, found, err := w.store.GetChatMappingByThreadID(ctx, w.installation.ID, threadID)
				if err != nil {
					log.Printf("connector: get chat mapping error: %v", err)
					return
				}
				if !found {
					log.Printf("connector: no chat mapping for thread %s", message.GetThreadId())
					shouldAck = true
				} else if mapping.BlockedAt != nil {
					shouldAck = true
				} else {
					blocked, err := w.deliverOutboundMessage(ctx, mapping, message)
					if blocked {
						marked, markErr := w.store.MarkChatBlocked(ctx, w.installation.ID, mapping.TelegramChatID)
						if markErr != nil {
							log.Printf("connector: mark chat blocked error: %v", markErr)
						}
						if marked {
							w.appendAudit(ctx, auditEvent{
								name:           auditEventBotBlocked,
								message:        fmt.Sprintf("%s: chat_id=%d", auditEventBotBlocked, mapping.TelegramChatID),
								level:          appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_INFO,
								idempotencyKey: auditKeyWithTime(auditEventBotBlocked, w.installation.ID, time.Now().UTC(), fmt.Sprintf("chat-%d", mapping.TelegramChatID)),
							})
						}
						shouldAck = true
					}
					if err != nil {
						if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							return
						}
						log.Printf("connector: deliver outbound error: %v", err)
						w.logOutboundFailure(ctx, message, err)
						shouldAck = true
					} else {
						shouldAck = true
					}
				}
			}
		}
		if shouldAck {
			w.ackMessage(ctx, message.GetId())
		}
	}
}

func (w *installationWorker) ackMessage(ctx context.Context, messageID string) {
	if err := w.gateway.AckMessages(ctx, []string{messageID}); err != nil {
		log.Printf("connector: ack message error: %v", err)
	}
}

func (w *installationWorker) deliverOutboundMessage(ctx context.Context, mapping store.ChatMapping, message *threadsv1.Message) (bool, error) {
	body, inlineImages := extractInlineImages(message.GetBody())
	if body != "" {
		blocked, err := w.sendWithRetry(ctx, func(ctx context.Context) error {
			return w.telegramClient.SendMessage(ctx, mapping.TelegramChatID, body)
		})
		if blocked || err != nil {
			return blocked, err
		}
	}
	for _, imageURL := range inlineImages {
		blocked, err := w.sendInlineImage(ctx, mapping.TelegramChatID, imageURL)
		if blocked || err != nil {
			return blocked, err
		}
	}
	for _, fileID := range message.GetFileIds() {
		blocked, err := w.sendPlatformFile(ctx, mapping.TelegramChatID, fileID)
		if blocked || err != nil {
			return blocked, err
		}
	}
	return false, nil
}

func (w *installationWorker) sendInlineImage(ctx context.Context, chatID int64, imageURL string) (bool, error) {
	if strings.HasPrefix(imageURL, "agyn://file/") {
		fileID := strings.TrimPrefix(imageURL, "agyn://file/")
		if fileID == "" {
			return false, fmt.Errorf("inline image missing file id")
		}
		return w.sendGatewayFile(ctx, chatID, fileID)
	}
	return w.sendWithRetry(ctx, func(ctx context.Context) error {
		return w.telegramClient.SendPhotoURL(ctx, chatID, imageURL)
	})
}

func (w *installationWorker) sendPlatformFile(ctx context.Context, chatID int64, fileID string) (bool, error) {
	if fileID == "" {
		return false, fmt.Errorf("file id missing")
	}
	return w.sendGatewayFile(ctx, chatID, fileID)
}

func (w *installationWorker) sendGatewayFile(ctx context.Context, chatID int64, fileID string) (bool, error) {
	metadata, payload, err := w.fetchGatewayFile(ctx, fileID)
	if err != nil {
		return false, err
	}
	method, field := telegramMethodForFile(metadata)
	filename := metadata.GetFilename()
	if filename == "" {
		filename = metadata.GetId()
	}
	contentType := metadata.GetContentType()
	return w.sendWithRetry(ctx, func(ctx context.Context) error {
		return w.telegramClient.SendFile(ctx, method, field, chatID, filename, contentType, payload)
	})
}

func (w *installationWorker) fetchGatewayFile(ctx context.Context, fileID string) (*filesv1.FileInfo, []byte, error) {
	attempts := 0
	for {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		metadata, err := w.gateway.GetFileMetadata(ctx, fileID)
		if err == nil {
			payload, payloadErr := w.gateway.GetFileContent(ctx, fileID)
			if payloadErr == nil {
				return metadata, payload, nil
			}
			err = fmt.Errorf("get file content: %w", payloadErr)
		} else {
			err = fmt.Errorf("get file metadata: %w", err)
		}
		attempts++
		if attempts > outboundRetryMax {
			return nil, nil, err
		}
		if !sleepContext(ctx, retryDelay(attempts)) {
			return nil, nil, ctx.Err()
		}
	}
}

func (w *installationWorker) logOutboundFailure(ctx context.Context, message *threadsv1.Message, err error) {
	if message == nil {
		return
	}
	if w.status != nil && w.status.TokenRejected() {
		var apiErr *telegram.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
			return
		}
	}
	if w.status != nil {
		if w.status.SetOutboundDegraded(true) {
			w.status.Report(ctx)
		}
	}
	detail := formatStatusError(err)
	if detail == "" {
		detail = "unknown"
	}
	key := auditKey(auditEventOutboundFailed, w.installation.ID.String(), message.GetId())
	if message.GetId() == "" {
		key = auditKeyWithTime(auditEventOutboundFailed, w.installation.ID, time.Now().UTC(), message.GetThreadId())
	}
	w.appendAudit(ctx, auditEvent{
		name:           auditEventOutboundFailed,
		message:        fmt.Sprintf("%s: thread_id=%s message_id=%s error=%s", auditEventOutboundFailed, message.GetThreadId(), message.GetId(), detail),
		level:          appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_ERROR,
		idempotencyKey: key,
	})
}

func (w *installationWorker) sendWithRetry(ctx context.Context, send func(context.Context) error) (bool, error) {
	rateBackoff := outboundRetryBase
	attempts := 0
	for {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		err := send(ctx)
		if err == nil {
			if w.status != nil {
				now := time.Now().UTC()
				w.status.RecordOutbound(now)
				if w.status.SetOutboundDegraded(false) {
					w.status.Report(ctx)
				}
			}
			return false, nil
		}
		var apiErr *telegram.APIError
		if errors.As(err, &apiErr) {
			if apiErr.StatusCode == http.StatusUnauthorized {
				now := time.Now().UTC()
				if w.status != nil {
					w.status.RecordError(now, formatStatusError(err))
				}
				w.handleTokenRejected(ctx, now)
				return false, err
			}
			if apiErr.IsBlocked() {
				if w.status != nil {
					w.status.RecordError(time.Now().UTC(), formatStatusError(err))
				}
				return true, err
			}
			if apiErr.IsRateLimit() {
				wait := apiErr.RetryAfter
				if wait == 0 {
					wait = rateBackoff
					rateBackoff = nextBackoff(rateBackoff)
				}
				if !sleepContext(ctx, wait) {
					return false, ctx.Err()
				}
				continue
			}
			if apiErr.IsTransient() {
				attempts++
				if attempts > outboundRetryMax {
					return false, err
				}
				if !sleepContext(ctx, retryDelay(attempts)) {
					return false, ctx.Err()
				}
				continue
			}
			if w.status != nil {
				w.status.RecordError(time.Now().UTC(), formatStatusError(err))
			}
			return false, err
		}
		attempts++
		if attempts > outboundRetryMax {
			if w.status != nil {
				w.status.RecordError(time.Now().UTC(), formatStatusError(err))
			}
			return false, err
		}
		if !sleepContext(ctx, retryDelay(attempts)) {
			return false, ctx.Err()
		}
	}
}

func telegramMethodForFile(metadata *filesv1.FileInfo) (string, string) {
	contentType := strings.ToLower(metadata.GetContentType())
	filename := strings.ToLower(metadata.GetFilename())
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return "sendPhoto", "photo"
	case strings.HasPrefix(contentType, "video/"):
		return "sendVideo", "video"
	case strings.HasPrefix(contentType, "audio/"):
		if contentType == "audio/ogg" || contentType == "audio/opus" || strings.HasSuffix(filename, ".ogg") {
			return "sendVoice", "voice"
		}
		return "sendAudio", "audio"
	default:
		return "sendDocument", "document"
	}
}
