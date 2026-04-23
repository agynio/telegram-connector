package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	"github.com/agynio/telegram-connector/internal/store"
	"github.com/agynio/telegram-connector/internal/telegram"
)

const (
	inboundRetryDelay     = time.Second
	inboundUploadRetryMax = 3
	degradedThreadMessage = "thread is degraded"
)

func (w *installationWorker) runInbound(ctx context.Context, state store.InstallationState) error {
	lastUpdateID := state.LastUpdateID
	offset := lastUpdateID + 1

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if w.status != nil && w.status.TokenRejected() {
			<-ctx.Done()
			return nil
		}

		updates, err := w.telegramClient.GetUpdates(ctx, offset, w.pollTimeout)
		now := time.Now().UTC()
		if err != nil {
			log.Printf("connector: getUpdates error: %v", err)
			w.handlePollError(ctx, now, err)
			if !sleepContext(ctx, inboundRetryDelay) {
				return nil
			}
			continue
		}
		if w.status != nil {
			w.status.RecordPollSuccess(now, lastUpdateID)
		}
		w.handlePollSuccess(ctx, now)
		if len(updates) == 0 {
			continue
		}

		maxUpdateID := lastUpdateID
		processingFailed := false
		for _, update := range updates {
			if update.UpdateID > maxUpdateID {
				maxUpdateID = update.UpdateID
			}
			if err := w.handleUpdate(ctx, update); err != nil {
				log.Printf("connector: handle update error: %v", err)
				if w.status != nil {
					w.status.RecordError(time.Now().UTC(), formatStatusError(err))
				}
				processingFailed = true
				break
			}
		}
		if processingFailed {
			if !sleepContext(ctx, inboundRetryDelay) {
				return nil
			}
			continue
		}
		if maxUpdateID > lastUpdateID {
			if err := w.persistInstallationState(ctx, maxUpdateID); err != nil {
				return err
			}
			lastUpdateID = maxUpdateID
			offset = lastUpdateID + 1
		}
		if w.status != nil {
			w.status.RecordPollSuccess(now, lastUpdateID)
		}
	}
}

func (w *installationWorker) handlePollError(ctx context.Context, now time.Time, err error) {
	if w.status != nil {
		w.status.RecordError(now, formatStatusError(err))
	}
	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		w.handleTokenRejected(ctx, now)
		return
	}
	if w.status != nil {
		if w.status.SetPollingDegraded(true) {
			w.status.Report(ctx)
		}
	}
	if w.telegramFailureAt.IsZero() {
		w.telegramFailureAt = now
	}
	if !w.telegramUnreachable && now.Sub(w.telegramFailureAt) >= telegramUnreachableTimeout {
		w.telegramUnreachable = true
		w.appendAudit(ctx, auditEvent{
			name:           auditEventTelegramUnreachable,
			message:        fmt.Sprintf("%s: getUpdates failing since %s", auditEventTelegramUnreachable, formatTimestamp(w.telegramFailureAt)),
			level:          appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_WARNING,
			idempotencyKey: auditKeyWithTime(auditEventTelegramUnreachable, w.installation.ID, w.telegramFailureAt),
		})
	}
}

func (w *installationWorker) handlePollSuccess(ctx context.Context, now time.Time) {
	wasUnreachable := w.telegramUnreachable
	stateChanged := false
	if w.status != nil {
		if w.status.SetTokenRejected(false) {
			stateChanged = true
		}
		if w.status.SetPollingDegraded(false) {
			stateChanged = true
		}
	}
	if wasUnreachable {
		w.appendAudit(ctx, auditEvent{
			name:           auditEventTelegramRecovered,
			message:        fmt.Sprintf("%s: getUpdates recovered at %s", auditEventTelegramRecovered, formatTimestamp(now)),
			level:          appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_INFO,
			idempotencyKey: auditKeyWithTime(auditEventTelegramRecovered, w.installation.ID, now),
		})
	}
	w.telegramFailureAt = time.Time{}
	w.telegramUnreachable = false
	if stateChanged && w.status != nil {
		w.status.Report(ctx)
	}
}

func (w *installationWorker) handleTokenRejected(ctx context.Context, now time.Time) {
	wasRejected := false
	if w.status != nil {
		wasRejected = w.status.TokenRejected()
		if w.status.SetTokenRejected(true) {
			w.status.Report(ctx)
		}
	}
	if !wasRejected {
		w.appendAudit(ctx, auditEvent{
			name:           auditEventBotTokenRejected,
			message:        fmt.Sprintf("%s: telegram returned 401 Unauthorized", auditEventBotTokenRejected),
			level:          appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_ERROR,
			idempotencyKey: auditKeyWithTime(auditEventBotTokenRejected, w.installation.ID, now),
		})
	}
}

func (w *installationWorker) loadInstallationState(ctx context.Context) (store.InstallationState, error) {
	for {
		state, err := w.store.GetInstallationState(ctx, w.installation.ID)
		if err == nil {
			return state, nil
		}
		log.Printf("connector: get installation state error: %v", err)
		if !sleepContext(ctx, inboundRetryDelay) {
			return store.InstallationState{}, ctx.Err()
		}
	}
}

func (w *installationWorker) persistInstallationState(ctx context.Context, updateID int64) error {
	for {
		if err := w.store.UpsertInstallationState(ctx, w.installation.ID, updateID); err != nil {
			log.Printf("connector: update installation state error: %v", err)
			if !sleepContext(ctx, inboundRetryDelay) {
				return ctx.Err()
			}
			continue
		}
		return nil
	}
}

func (w *installationWorker) handleUpdate(ctx context.Context, update telegram.Update) error {
	if update.Message == nil {
		return nil
	}
	message := update.Message
	if message.From == nil {
		return nil
	}
	if message.Chat.Type != "private" {
		return nil
	}

	mapping, found, err := w.store.GetChatMapping(ctx, w.installation.ID, message.Chat.ID)
	if err != nil {
		return err
	}
	if found && mapping.BlockedAt != nil {
		cleared, err := w.store.ClearChatBlocked(ctx, w.installation.ID, message.Chat.ID)
		if err != nil {
			return err
		}
		if cleared {
			w.appendAudit(ctx, auditEvent{
				name:           auditEventBotUnblocked,
				message:        fmt.Sprintf("%s: chat_id=%d", auditEventBotUnblocked, message.Chat.ID),
				level:          appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_INFO,
				idempotencyKey: auditKeyWithTime(auditEventBotUnblocked, w.installation.ID, time.Now().UTC(), fmt.Sprintf("chat-%d", message.Chat.ID)),
			})
		}
	}
	if !found {
		var err error
		mapping, err = w.createThreadMapping(ctx, message.Chat.ID, message.From.ID)
		if err != nil {
			return err
		}
	}

	body, fileIDs, err := w.buildInboundMessage(ctx, message)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" && len(fileIDs) == 0 {
		log.Printf("connector: skip empty inbound message for chat %d", message.Chat.ID)
		return nil
	}
	threadID := mapping.ThreadID
	if err := w.gateway.SendMessage(ctx, threadID.String(), body, fileIDs); err != nil {
		if !isThreadDegradedError(err) {
			return err
		}
		if w.status != nil {
			w.status.RecordError(time.Now().UTC(), formatStatusError(err))
		}
		log.Printf("connector: thread %s degraded for chat %d", threadID.String(), message.Chat.ID)
		oldThreadID := threadID
		mapping, err = w.remapThreadMapping(ctx, message)
		if err != nil {
			return err
		}
		threadID = mapping.ThreadID
		w.appendAudit(ctx, auditEvent{
			name:           auditEventThreadDegraded,
			message:        fmt.Sprintf("%s: chat_id=%d old_thread_id=%s new_thread_id=%s", auditEventThreadDegraded, message.Chat.ID, oldThreadID.String(), threadID.String()),
			level:          appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_WARNING,
			idempotencyKey: auditKey(auditEventThreadDegraded, w.installation.ID.String(), oldThreadID.String(), threadID.String(), fmt.Sprintf("chat-%d", message.Chat.ID)),
		})
		if err := w.gateway.SendMessage(ctx, threadID.String(), body, fileIDs); err != nil {
			if isThreadDegradedError(err) {
				log.Printf("connector: remapped thread %s degraded for chat %d", threadID.String(), message.Chat.ID)
				if w.status != nil {
					w.status.RecordError(time.Now().UTC(), formatStatusError(err))
				}
				return nil
			}
			return err
		}
	}
	if w.status != nil {
		w.status.RecordInbound(time.Now().UTC())
	}
	return nil
}

func (w *installationWorker) createThreadMapping(ctx context.Context, chatID, userID int64) (store.ChatMapping, error) {
	thread, err := w.gateway.CreateThread(ctx, w.installation.OrganizationID.String())
	if err != nil {
		return store.ChatMapping{}, err
	}
	threadUUID, err := uuid.Parse(thread.GetId())
	if err != nil {
		return store.ChatMapping{}, fmt.Errorf("invalid thread id: %w", err)
	}
	if err := w.gateway.AddParticipant(ctx, w.installation.OrganizationID.String(), thread.GetId(), w.installation.AgentID.String()); err != nil {
		return store.ChatMapping{}, err
	}
	mapping, err := w.store.CreateChatMapping(ctx, store.ChatMappingInput{
		InstallationID: w.installation.ID,
		TelegramChatID: chatID,
		TelegramUserID: userID,
		ThreadID:       threadUUID,
	})
	if err != nil {
		return store.ChatMapping{}, err
	}
	return mapping, nil
}

func (w *installationWorker) remapThreadMapping(ctx context.Context, message *telegram.Message) (store.ChatMapping, error) {
	if err := w.store.DeleteChatMapping(ctx, w.installation.ID, message.Chat.ID); err != nil {
		return store.ChatMapping{}, err
	}
	return w.createThreadMapping(ctx, message.Chat.ID, message.From.ID)
}

func (w *installationWorker) buildInboundMessage(ctx context.Context, message *telegram.Message) (string, []string, error) {
	if message.Text != "" {
		return message.Text, nil, nil
	}

	caption := strings.TrimSpace(message.Caption)
	if len(message.Photo) > 0 {
		photo := largestPhoto(message.Photo)
		fileID, err := w.uploadTelegramFile(ctx, photo.FileID, "photo", "image/jpeg", true)
		if err != nil {
			return "", nil, err
		}
		return caption, fileIDsOrNil(fileID), nil
	}
	if message.Document != nil {
		fileID, err := w.uploadTelegramFile(ctx, message.Document.FileID, message.Document.FileName, message.Document.MimeType, false)
		if err != nil {
			return "", nil, err
		}
		return caption, fileIDsOrNil(fileID), nil
	}
	if message.Audio != nil {
		fileID, err := w.uploadTelegramFile(ctx, message.Audio.FileID, message.Audio.FileName, message.Audio.MimeType, false)
		if err != nil {
			return "", nil, err
		}
		return caption, fileIDsOrNil(fileID), nil
	}
	if message.Video != nil {
		fileID, err := w.uploadTelegramFile(ctx, message.Video.FileID, message.Video.FileName, message.Video.MimeType, false)
		if err != nil {
			return "", nil, err
		}
		return caption, fileIDsOrNil(fileID), nil
	}
	if message.Voice != nil {
		fileID, err := w.uploadTelegramFile(ctx, message.Voice.FileID, "voice", message.Voice.MimeType, false)
		if err != nil {
			return "", nil, err
		}
		return caption, fileIDsOrNil(fileID), nil
	}
	if message.Sticker != nil {
		label := "[sticker]"
		if message.Sticker.Emoji != "" {
			label = fmt.Sprintf("[sticker %s]", message.Sticker.Emoji)
		}
		return label, nil, nil
	}
	return "[unsupported message]", nil, nil
}

func fileIDsOrNil(fileID string) []string {
	if strings.TrimSpace(fileID) == "" {
		return nil
	}
	return []string{fileID}
}

var errUploadContentTypeNotAllowed = errors.New("upload content_type not allowed")

func (w *installationWorker) uploadTelegramFile(ctx context.Context, fileID, filename, mimeType string, requireImage bool) (string, error) {
	file, err := w.telegramClient.GetFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	payload, headerContentType, err := w.telegramClient.DownloadFile(ctx, file.FilePath)
	if err != nil {
		return "", err
	}
	sniffedContentType := http.DetectContentType(payload)
	extensionType := extensionContentType(file.FilePath, filename)
	candidates := buildContentTypeCandidates(mimeType, sniffedContentType, extensionType, headerContentType)
	allowedContentTypes := selectAllowedContentTypes(candidates, requireImage)
	if len(allowedContentTypes) == 0 {
		w.logAttachmentSkipped(ctx, fileID, candidates)
		return "", nil
	}
	if filename == "" {
		filename = fileID
	}
	for _, contentType := range allowedContentTypes {
		metadata := &filesv1.UploadFileMetadata{
			Filename:    filename,
			ContentType: contentType,
			SizeBytes:   int64(len(payload)),
		}
		uploaded, err := w.uploadWithRetries(ctx, metadata, payload)
		if err == nil {
			return uploaded.GetId(), nil
		}
		if errors.Is(err, errUploadContentTypeNotAllowed) {
			continue
		}
		return "", err
	}
	w.logAttachmentSkipped(ctx, fileID, candidates)
	return "", nil
}

func (w *installationWorker) uploadWithRetries(ctx context.Context, metadata *filesv1.UploadFileMetadata, payload []byte) (*filesv1.FileInfo, error) {
	attempts := 0
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		uploaded, err := w.gateway.UploadFile(ctx, metadata, payload)
		if err == nil {
			return uploaded, nil
		}
		if isUploadContentTypeNotAllowed(err) {
			return nil, errUploadContentTypeNotAllowed
		}
		attempts++
		if !isUploadRetriableError(err) || attempts >= inboundUploadRetryMax {
			return nil, err
		}
		if !sleepContext(ctx, retryDelay(attempts)) {
			return nil, ctx.Err()
		}
	}
}

func isUploadContentTypeNotAllowed(err error) bool {
	if err == nil {
		return false
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		if connectErr.Code() != connect.CodeInvalidArgument {
			return false
		}
		return strings.Contains(strings.ToLower(connectErr.Message()), "content_type is not allowed")
	}
	return strings.Contains(strings.ToLower(err.Error()), "content_type is not allowed")
}

func isUploadRetriableError(err error) bool {
	if errors.Is(err, io.EOF) {
		return true
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		switch connectErr.Code() {
		case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeInternal:
			return true
		}
	}
	return false
}

func (w *installationWorker) logAttachmentSkipped(ctx context.Context, fileID string, candidates []contentTypeCandidate) {
	summary := formatContentTypeCandidates(candidates)
	if strings.TrimSpace(summary) == "" {
		summary = "none"
	}
	message := fmt.Sprintf("%s: file_id=%s content_types=%s", auditEventAttachmentSkipped, fileID, summary)
	log.Printf("connector: attachment skipped: file_id=%s content_types=%s", fileID, summary)
	if w.status != nil {
		w.status.RecordError(time.Now().UTC(), message)
	}
	w.appendAudit(ctx, auditEvent{
		name:           auditEventAttachmentSkipped,
		message:        message,
		level:          appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_WARNING,
		idempotencyKey: auditKeyWithHash(auditEventAttachmentSkipped, w.installation.ID, fmt.Sprintf("%s:%s", fileID, summary)),
	})
}

func isThreadDegradedError(err error) bool {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr.Code() == connect.CodeFailedPrecondition && connectErr.Message() == degradedThreadMessage
	}
	return false
}

func largestPhoto(photos []telegram.Photo) telegram.Photo {
	largest := photos[0]
	for _, photo := range photos[1:] {
		if photo.FileSize > largest.FileSize {
			largest = photo
		}
	}
	return largest
}
