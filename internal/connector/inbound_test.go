package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	notificationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/notifications/v1"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
	"github.com/agynio/telegram-connector/internal/store"
	"github.com/agynio/telegram-connector/internal/telegram"
)

type stubGateway struct {
	t                *testing.T
	appIdentityID    string
	createThreadFn   func(context.Context, string) (*threadsv1.Thread, error)
	addParticipantFn func(context.Context, string, string, string) error
	sendMessageFn    func(context.Context, string, string, []string) error
	reportStatusFn   func(context.Context, string, string) error
	appendAuditFn    func(context.Context, string, string, appsv1.InstallationAuditLogLevel, string) error
}

func (s *stubGateway) AppIdentityID() string {
	return s.appIdentityID
}

func (s *stubGateway) ListInstallations(ctx context.Context, appID string) ([]*appsv1.Installation, error) {
	s.t.Fatalf("unexpected ListInstallations")
	return nil, nil
}

func (s *stubGateway) CreateThread(ctx context.Context, organizationID string) (*threadsv1.Thread, error) {
	if s.createThreadFn == nil {
		s.t.Fatalf("unexpected CreateThread")
	}
	return s.createThreadFn(ctx, organizationID)
}

func (s *stubGateway) AddParticipant(ctx context.Context, organizationID, threadID, participantID string) error {
	if s.addParticipantFn == nil {
		s.t.Fatalf("unexpected AddParticipant")
	}
	return s.addParticipantFn(ctx, organizationID, threadID, participantID)
}

func (s *stubGateway) SendMessage(ctx context.Context, threadID, body string, fileIDs []string) error {
	if s.sendMessageFn == nil {
		s.t.Fatalf("unexpected SendMessage")
	}
	return s.sendMessageFn(ctx, threadID, body, fileIDs)
}

func (s *stubGateway) GetUnackedMessages(ctx context.Context) ([]*threadsv1.Message, error) {
	s.t.Fatalf("unexpected GetUnackedMessages")
	return nil, nil
}

func (s *stubGateway) AckMessages(ctx context.Context, messageIDs []string) error {
	s.t.Fatalf("unexpected AckMessages")
	return nil
}

func (s *stubGateway) Subscribe(ctx context.Context) (*connect.ServerStreamForClient[notificationsv1.SubscribeResponse], error) {
	s.t.Fatalf("unexpected Subscribe")
	return nil, nil
}

func (s *stubGateway) UploadFile(ctx context.Context, metadata *filesv1.UploadFileMetadata, payload []byte) (*filesv1.FileInfo, error) {
	s.t.Fatalf("unexpected UploadFile")
	return nil, nil
}

func (s *stubGateway) GetFileMetadata(ctx context.Context, fileID string) (*filesv1.FileInfo, error) {
	s.t.Fatalf("unexpected GetFileMetadata")
	return nil, nil
}

func (s *stubGateway) GetFileContent(ctx context.Context, fileID string) ([]byte, error) {
	s.t.Fatalf("unexpected GetFileContent")
	return nil, nil
}

func (s *stubGateway) ReportInstallationStatus(ctx context.Context, installationID, status string) error {
	if s.reportStatusFn == nil {
		s.t.Fatalf("unexpected ReportInstallationStatus")
	}
	return s.reportStatusFn(ctx, installationID, status)
}

func (s *stubGateway) AppendInstallationAuditLogEntry(ctx context.Context, installationID, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey string) error {
	if s.appendAuditFn == nil {
		s.t.Fatalf("unexpected AppendInstallationAuditLogEntry")
	}
	return s.appendAuditFn(ctx, installationID, message, level, idempotencyKey)
}

type stubStore struct {
	t                      *testing.T
	getChatMappingFn       func(context.Context, uuid.UUID, int64) (store.ChatMapping, bool, error)
	createChatMappingFn    func(context.Context, store.ChatMappingInput) (store.ChatMapping, error)
	deleteChatMappingFn    func(context.Context, uuid.UUID, int64) error
	clearChatBlockedFn     func(context.Context, uuid.UUID, int64) (bool, error)
	markChatBlockedFn      func(context.Context, uuid.UUID, int64) (bool, error)
	countActiveChatsFn     func(context.Context, uuid.UUID) (int64, error)
	countBlockedChatsFn    func(context.Context, uuid.UUID) (int64, error)
	getInstallationStateFn func(context.Context, uuid.UUID) (store.InstallationState, error)
	upsertStateFn          func(context.Context, uuid.UUID, int64) error
}

func (s *stubStore) GetChatMapping(ctx context.Context, installationID uuid.UUID, chatID int64) (store.ChatMapping, bool, error) {
	if s.getChatMappingFn == nil {
		s.t.Fatalf("unexpected GetChatMapping")
	}
	return s.getChatMappingFn(ctx, installationID, chatID)
}

func (s *stubStore) GetChatMappingByThreadID(ctx context.Context, installationID, threadID uuid.UUID) (store.ChatMapping, bool, error) {
	s.t.Fatalf("unexpected GetChatMappingByThreadID")
	return store.ChatMapping{}, false, nil
}

func (s *stubStore) CreateChatMapping(ctx context.Context, input store.ChatMappingInput) (store.ChatMapping, error) {
	if s.createChatMappingFn == nil {
		s.t.Fatalf("unexpected CreateChatMapping")
	}
	return s.createChatMappingFn(ctx, input)
}

func (s *stubStore) DeleteChatMapping(ctx context.Context, installationID uuid.UUID, chatID int64) error {
	if s.deleteChatMappingFn == nil {
		s.t.Fatalf("unexpected DeleteChatMapping")
	}
	return s.deleteChatMappingFn(ctx, installationID, chatID)
}

func (s *stubStore) ClearChatBlocked(ctx context.Context, installationID uuid.UUID, chatID int64) (bool, error) {
	if s.clearChatBlockedFn == nil {
		s.t.Fatalf("unexpected ClearChatBlocked")
	}
	return s.clearChatBlockedFn(ctx, installationID, chatID)
}

func (s *stubStore) MarkChatBlocked(ctx context.Context, installationID uuid.UUID, chatID int64) (bool, error) {
	if s.markChatBlockedFn == nil {
		s.t.Fatalf("unexpected MarkChatBlocked")
	}
	return s.markChatBlockedFn(ctx, installationID, chatID)
}

func (s *stubStore) CountActiveChats(ctx context.Context, installationID uuid.UUID) (int64, error) {
	if s.countActiveChatsFn == nil {
		s.t.Fatalf("unexpected CountActiveChats")
	}
	return s.countActiveChatsFn(ctx, installationID)
}

func (s *stubStore) CountBlockedChats(ctx context.Context, installationID uuid.UUID) (int64, error) {
	if s.countBlockedChatsFn == nil {
		s.t.Fatalf("unexpected CountBlockedChats")
	}
	return s.countBlockedChatsFn(ctx, installationID)
}

func (s *stubStore) GetInstallationState(ctx context.Context, installationID uuid.UUID) (store.InstallationState, error) {
	if s.getInstallationStateFn == nil {
		s.t.Fatalf("unexpected GetInstallationState")
	}
	return s.getInstallationStateFn(ctx, installationID)
}

func (s *stubStore) UpsertInstallationState(ctx context.Context, installationID uuid.UUID, lastUpdateID int64) error {
	if s.upsertStateFn == nil {
		s.t.Fatalf("unexpected UpsertInstallationState")
	}
	return s.upsertStateFn(ctx, installationID, lastUpdateID)
}

func TestIsThreadDegradedError(t *testing.T) {
	err := fmt.Errorf("send message: %w", connect.NewError(connect.CodeFailedPrecondition, errors.New(degradedThreadMessage)))
	if !isThreadDegradedError(err) {
		t.Fatal("expected degraded error")
	}
	notDegraded := connect.NewError(connect.CodeFailedPrecondition, errors.New("other"))
	if isThreadDegradedError(notDegraded) {
		t.Fatal("expected non-degraded error")
	}
}

func TestHandleUpdateRemapsOnDegradedThread(t *testing.T) {
	installationID := uuid.New()
	organizationID := uuid.New()
	agentID := uuid.New()
	chatID := int64(44)
	userID := int64(77)
	oldThreadID := uuid.New()
	newThreadID := uuid.New()
	degradedErr := fmt.Errorf("send message: %w", connect.NewError(connect.CodeFailedPrecondition, errors.New(degradedThreadMessage)))

	deleteCalled := false
	createMappingCalled := false
	sendCalls := make([]string, 0, 2)
	addParticipantCalled := false
	createThreadCalled := false
	auditCalls := 0

	storeStub := &stubStore{
		t: t,
		getChatMappingFn: func(_ context.Context, installationArg uuid.UUID, chatIDArg int64) (store.ChatMapping, bool, error) {
			if installationArg != installationID {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if chatIDArg != chatID {
				t.Fatalf("expected chat %d, got %d", chatID, chatIDArg)
			}
			return store.ChatMapping{
				InstallationID: installationID,
				TelegramChatID: chatID,
				TelegramUserID: userID,
				ThreadID:       oldThreadID,
			}, true, nil
		},
		deleteChatMappingFn: func(_ context.Context, installationArg uuid.UUID, chatIDArg int64) error {
			deleteCalled = true
			if installationArg != installationID {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if chatIDArg != chatID {
				t.Fatalf("expected chat %d, got %d", chatID, chatIDArg)
			}
			return nil
		},
		createChatMappingFn: func(_ context.Context, input store.ChatMappingInput) (store.ChatMapping, error) {
			createMappingCalled = true
			if input.InstallationID != installationID {
				t.Fatalf("expected installation %s, got %s", installationID, input.InstallationID)
			}
			if input.TelegramChatID != chatID {
				t.Fatalf("expected chat %d, got %d", chatID, input.TelegramChatID)
			}
			if input.TelegramUserID != userID {
				t.Fatalf("expected user %d, got %d", userID, input.TelegramUserID)
			}
			if input.ThreadID != newThreadID {
				t.Fatalf("expected thread %s, got %s", newThreadID, input.ThreadID)
			}
			return store.ChatMapping{
				InstallationID: installationID,
				TelegramChatID: chatID,
				TelegramUserID: userID,
				ThreadID:       newThreadID,
				CreatedAt:      time.Now().UTC(),
			}, nil
		},
		clearChatBlockedFn: func(context.Context, uuid.UUID, int64) (bool, error) {
			t.Fatal("unexpected ClearChatBlocked")
			return false, nil
		},
		markChatBlockedFn: func(context.Context, uuid.UUID, int64) (bool, error) {
			t.Fatal("unexpected MarkChatBlocked")
			return false, nil
		},
		getInstallationStateFn: func(context.Context, uuid.UUID) (store.InstallationState, error) {
			t.Fatal("unexpected GetInstallationState")
			return store.InstallationState{}, nil
		},
		upsertStateFn: func(context.Context, uuid.UUID, int64) error {
			t.Fatal("unexpected UpsertInstallationState")
			return nil
		},
	}

	gatewayStub := &stubGateway{
		t: t,
		createThreadFn: func(_ context.Context, organizationArg string) (*threadsv1.Thread, error) {
			createThreadCalled = true
			if organizationArg != organizationID.String() {
				t.Fatalf("expected organization %s, got %s", organizationID, organizationArg)
			}
			return &threadsv1.Thread{Id: newThreadID.String()}, nil
		},
		addParticipantFn: func(_ context.Context, organizationArg, threadArg, participantArg string) error {
			addParticipantCalled = true
			if organizationArg != organizationID.String() {
				t.Fatalf("expected organization %s, got %s", organizationID, organizationArg)
			}
			if threadArg != newThreadID.String() {
				t.Fatalf("expected thread %s, got %s", newThreadID, threadArg)
			}
			if participantArg != agentID.String() {
				t.Fatalf("expected agent %s, got %s", agentID, participantArg)
			}
			return nil
		},
		sendMessageFn: func(_ context.Context, threadArg, body string, fileIDs []string) error {
			sendCalls = append(sendCalls, threadArg)
			if threadArg == oldThreadID.String() {
				return degradedErr
			}
			if threadArg == newThreadID.String() {
				return nil
			}
			return fmt.Errorf("unexpected thread %s", threadArg)
		},
		appendAuditFn: func(_ context.Context, installationArg, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey string) error {
			auditCalls++
			if installationArg != installationID.String() {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if level != appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_WARNING {
				t.Fatalf("expected warning level, got %v", level)
			}
			if !strings.Contains(message, auditEventThreadDegraded) {
				t.Fatalf("expected audit message to include %s, got %s", auditEventThreadDegraded, message)
			}
			if !strings.Contains(message, oldThreadID.String()) || !strings.Contains(message, newThreadID.String()) {
				t.Fatalf("expected audit message to include thread ids, got %s", message)
			}
			if strings.TrimSpace(idempotencyKey) == "" {
				t.Fatal("expected idempotency key")
			}
			return nil
		},
	}

	worker := newInstallationWorker(Installation{
		ID:             installationID,
		OrganizationID: organizationID,
		BotToken:       "token",
		AgentID:        agentID,
	}, storeStub, gatewayStub, "http://telegram.test", time.Second)

	update := telegram.Update{Message: &telegram.Message{
		From: &telegram.User{ID: userID},
		Chat: telegram.Chat{ID: chatID, Type: "private"},
		Text: "hello",
	}}

	if err := worker.handleUpdate(context.Background(), update); err != nil {
		t.Fatalf("handleUpdate returned error: %v", err)
	}
	if !deleteCalled {
		t.Fatal("expected DeleteChatMapping to be called")
	}
	if !createThreadCalled {
		t.Fatal("expected CreateThread to be called")
	}
	if !addParticipantCalled {
		t.Fatal("expected AddParticipant to be called")
	}
	if !createMappingCalled {
		t.Fatal("expected CreateChatMapping to be called")
	}
	if len(sendCalls) != 2 {
		t.Fatalf("expected 2 SendMessage calls, got %d", len(sendCalls))
	}
	if sendCalls[0] != oldThreadID.String() || sendCalls[1] != newThreadID.String() {
		t.Fatalf("expected send to old then new thread, got %v", sendCalls)
	}
	if auditCalls != 1 {
		t.Fatalf("expected 1 audit call, got %d", auditCalls)
	}
}

func TestHandleUpdateStopsAfterSecondDegradedSend(t *testing.T) {
	installationID := uuid.New()
	organizationID := uuid.New()
	agentID := uuid.New()
	chatID := int64(11)
	userID := int64(22)
	oldThreadID := uuid.New()
	newThreadID := uuid.New()
	degradedErr := fmt.Errorf("send message: %w", connect.NewError(connect.CodeFailedPrecondition, errors.New(degradedThreadMessage)))

	storeStub := &stubStore{
		t: t,
		getChatMappingFn: func(context.Context, uuid.UUID, int64) (store.ChatMapping, bool, error) {
			return store.ChatMapping{
				InstallationID: installationID,
				TelegramChatID: chatID,
				TelegramUserID: userID,
				ThreadID:       oldThreadID,
			}, true, nil
		},
		deleteChatMappingFn: func(context.Context, uuid.UUID, int64) error {
			return nil
		},
		createChatMappingFn: func(context.Context, store.ChatMappingInput) (store.ChatMapping, error) {
			return store.ChatMapping{ThreadID: newThreadID}, nil
		},
		clearChatBlockedFn: func(context.Context, uuid.UUID, int64) (bool, error) {
			return false, nil
		},
		markChatBlockedFn: func(context.Context, uuid.UUID, int64) (bool, error) {
			t.Fatal("unexpected MarkChatBlocked")
			return false, nil
		},
		getInstallationStateFn: func(context.Context, uuid.UUID) (store.InstallationState, error) {
			t.Fatal("unexpected GetInstallationState")
			return store.InstallationState{}, nil
		},
		upsertStateFn: func(context.Context, uuid.UUID, int64) error {
			t.Fatal("unexpected UpsertInstallationState")
			return nil
		},
	}

	gatewayStub := &stubGateway{
		t: t,
		createThreadFn: func(context.Context, string) (*threadsv1.Thread, error) {
			return &threadsv1.Thread{Id: newThreadID.String()}, nil
		},
		addParticipantFn: func(context.Context, string, string, string) error {
			return nil
		},
		sendMessageFn: func(context.Context, string, string, []string) error {
			return degradedErr
		},
		appendAuditFn: func(context.Context, string, string, appsv1.InstallationAuditLogLevel, string) error {
			return nil
		},
	}

	worker := newInstallationWorker(Installation{
		ID:             installationID,
		OrganizationID: organizationID,
		BotToken:       "token",
		AgentID:        agentID,
	}, storeStub, gatewayStub, "http://telegram.test", time.Second)

	update := telegram.Update{Message: &telegram.Message{
		From: &telegram.User{ID: userID},
		Chat: telegram.Chat{ID: chatID, Type: "private"},
		Text: "hello",
	}}

	if err := worker.handleUpdate(context.Background(), update); err != nil {
		t.Fatalf("handleUpdate returned error: %v", err)
	}
}

func TestHandleUpdateAttachmentSkipSendsMarkerWithoutCaption(t *testing.T) {
	installationID := uuid.New()
	organizationID := uuid.New()
	agentID := uuid.New()
	chatID := int64(55)
	userID := int64(99)
	threadID := uuid.New()
	fileID := "file-123"
	filePath := "files/" + fileID + ".bin"
	data := make([]byte, 512)

	server := newTelegramFileServer(t, "token", fileID, filePath, "application/octet-stream", data)
	defer server.Close()

	sendCalled := false
	auditCalled := false
	storeStub := &stubStore{
		t: t,
		getChatMappingFn: func(_ context.Context, installationArg uuid.UUID, chatIDArg int64) (store.ChatMapping, bool, error) {
			if installationArg != installationID {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if chatIDArg != chatID {
				t.Fatalf("expected chat %d, got %d", chatID, chatIDArg)
			}
			return store.ChatMapping{
				InstallationID: installationID,
				TelegramChatID: chatID,
				TelegramUserID: userID,
				ThreadID:       threadID,
			}, true, nil
		},
	}
	gatewayStub := &stubGateway{
		t: t,
		sendMessageFn: func(_ context.Context, threadArg, body string, fileIDs []string) error {
			sendCalled = true
			if threadArg != threadID.String() {
				t.Fatalf("expected thread %s, got %s", threadID, threadArg)
			}
			if len(fileIDs) != 0 {
				t.Fatalf("expected no file IDs, got %v", fileIDs)
			}
			if !strings.Contains(body, "attachment skipped: unsupported file type") {
				t.Fatalf("expected marker in body, got %s", body)
			}
			if !strings.Contains(body, "name=file.bin") {
				t.Fatalf("expected filename in body, got %s", body)
			}
			if !strings.Contains(body, "size=512") {
				t.Fatalf("expected size in body, got %s", body)
			}
			if !strings.Contains(body, "content_type=application/octet-stream") {
				t.Fatalf("expected content type in body, got %s", body)
			}
			return nil
		},
		appendAuditFn: func(_ context.Context, installationArg, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey string) error {
			auditCalled = true
			if installationArg != installationID.String() {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if strings.TrimSpace(idempotencyKey) == "" {
				t.Fatal("expected idempotency key")
			}
			return nil
		},
	}

	worker := newInstallationWorker(Installation{
		ID:             installationID,
		OrganizationID: organizationID,
		BotToken:       "token",
		AgentID:        agentID,
	}, storeStub, gatewayStub, server.URL, time.Second)

	update := telegram.Update{Message: &telegram.Message{
		From: &telegram.User{ID: userID},
		Chat: telegram.Chat{ID: chatID, Type: "private"},
		Document: &telegram.Document{
			FileID:   fileID,
			FileName: "file.bin",
			MimeType: "",
			FileSize: int64(len(data)),
		},
	}}

	if err := worker.handleUpdate(context.Background(), update); err != nil {
		t.Fatalf("handleUpdate returned error: %v", err)
	}
	if !sendCalled {
		t.Fatal("expected SendMessage to be called")
	}
	if !auditCalled {
		t.Fatal("expected audit log call")
	}
}

func TestHandleUpdateAttachmentSkipAppendsMarkerToCaption(t *testing.T) {
	installationID := uuid.New()
	organizationID := uuid.New()
	agentID := uuid.New()
	chatID := int64(56)
	userID := int64(100)
	threadID := uuid.New()
	fileID := "file-456"
	filePath := "files/" + fileID + ".bin"
	data := make([]byte, 256)
	caption := "caption text"

	server := newTelegramFileServer(t, "token", fileID, filePath, "application/octet-stream", data)
	defer server.Close()

	storeStub := &stubStore{
		t: t,
		getChatMappingFn: func(_ context.Context, installationArg uuid.UUID, chatIDArg int64) (store.ChatMapping, bool, error) {
			return store.ChatMapping{
				InstallationID: installationID,
				TelegramChatID: chatID,
				TelegramUserID: userID,
				ThreadID:       threadID,
			}, true, nil
		},
	}
	gatewayStub := &stubGateway{
		t: t,
		sendMessageFn: func(_ context.Context, threadArg, body string, fileIDs []string) error {
			if threadArg != threadID.String() {
				t.Fatalf("expected thread %s, got %s", threadID, threadArg)
			}
			if len(fileIDs) != 0 {
				t.Fatalf("expected no file IDs, got %v", fileIDs)
			}
			if !strings.HasPrefix(body, caption+"\n") {
				t.Fatalf("expected caption prefix, got %s", body)
			}
			if !strings.Contains(body, "attachment skipped: unsupported file type") {
				t.Fatalf("expected marker in body, got %s", body)
			}
			return nil
		},
		appendAuditFn: func(context.Context, string, string, appsv1.InstallationAuditLogLevel, string) error {
			return nil
		},
	}

	worker := newInstallationWorker(Installation{
		ID:             installationID,
		OrganizationID: organizationID,
		BotToken:       "token",
		AgentID:        agentID,
	}, storeStub, gatewayStub, server.URL, time.Second)

	update := telegram.Update{Message: &telegram.Message{
		From:    &telegram.User{ID: userID},
		Chat:    telegram.Chat{ID: chatID, Type: "private"},
		Caption: caption,
		Document: &telegram.Document{
			FileID:   fileID,
			FileName: "file.bin",
			MimeType: "",
			FileSize: int64(len(data)),
		},
	}}

	if err := worker.handleUpdate(context.Background(), update); err != nil {
		t.Fatalf("handleUpdate returned error: %v", err)
	}
}
