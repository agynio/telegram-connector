package connector

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	"github.com/agynio/telegram-connector/internal/store"
	"github.com/agynio/telegram-connector/internal/telegram"
)

const (
	inboundRetryDelay     = time.Second
	degradedThreadMessage = "thread is degraded"
)

func (w *installationWorker) runInbound(ctx context.Context) error {
	lastUpdateID, err := w.loadInstallationState(ctx)
	if err != nil {
		return err
	}
	offset := lastUpdateID + 1

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		updates, err := w.telegramClient.GetUpdates(ctx, offset, w.pollTimeout)
		if err != nil {
			log.Printf("connector: getUpdates error: %v", err)
			if !sleepContext(ctx, inboundRetryDelay) {
				return nil
			}
			continue
		}
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
	}
}

func (w *installationWorker) loadInstallationState(ctx context.Context) (int64, error) {
	for {
		state, err := w.store.GetInstallationState(ctx, w.installation.ID)
		if err == nil {
			return state, nil
		}
		log.Printf("connector: get installation state error: %v", err)
		if !sleepContext(ctx, inboundRetryDelay) {
			return 0, ctx.Err()
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
		if err := w.store.ClearChatBlocked(ctx, w.installation.ID, message.Chat.ID); err != nil {
			return err
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
	threadID := mapping.ThreadID
	if err := w.gateway.SendMessage(ctx, threadID.String(), body, fileIDs); err != nil {
		if !isThreadDegradedError(err) {
			return err
		}
		log.Printf("connector: thread %s degraded for chat %d", threadID.String(), message.Chat.ID)
		mapping, err = w.remapThreadMapping(ctx, message)
		if err != nil {
			return err
		}
		threadID = mapping.ThreadID
		if err := w.gateway.SendMessage(ctx, threadID.String(), body, fileIDs); err != nil {
			if isThreadDegradedError(err) {
				log.Printf("connector: remapped thread %s degraded for chat %d", threadID.String(), message.Chat.ID)
				return nil
			}
			return err
		}
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
		fileID, err := w.uploadTelegramFile(ctx, photo.FileID, "photo", "image/jpeg")
		if err != nil {
			return "", nil, err
		}
		return caption, []string{fileID}, nil
	}
	if message.Document != nil {
		fileID, err := w.uploadTelegramFile(ctx, message.Document.FileID, message.Document.FileName, message.Document.MimeType)
		if err != nil {
			return "", nil, err
		}
		return caption, []string{fileID}, nil
	}
	if message.Audio != nil {
		fileID, err := w.uploadTelegramFile(ctx, message.Audio.FileID, message.Audio.FileName, message.Audio.MimeType)
		if err != nil {
			return "", nil, err
		}
		return caption, []string{fileID}, nil
	}
	if message.Video != nil {
		fileID, err := w.uploadTelegramFile(ctx, message.Video.FileID, message.Video.FileName, message.Video.MimeType)
		if err != nil {
			return "", nil, err
		}
		return caption, []string{fileID}, nil
	}
	if message.Voice != nil {
		fileID, err := w.uploadTelegramFile(ctx, message.Voice.FileID, "voice", message.Voice.MimeType)
		if err != nil {
			return "", nil, err
		}
		return caption, []string{fileID}, nil
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

func (w *installationWorker) uploadTelegramFile(ctx context.Context, fileID, filename, mimeType string) (string, error) {
	file, err := w.telegramClient.GetFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	payload, contentType, err := w.telegramClient.DownloadFile(ctx, file.FilePath)
	if err != nil {
		return "", err
	}
	if contentType == "" {
		contentType = mimeType
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if filename == "" {
		filename = fileID
	}
	metadata := &filesv1.UploadFileMetadata{
		Filename:    filename,
		ContentType: contentType,
		SizeBytes:   int64(len(payload)),
	}
	uploaded, err := w.gateway.UploadFile(ctx, metadata, payload)
	if err != nil {
		return "", err
	}
	return uploaded.GetId(), nil
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
