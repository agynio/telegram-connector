package connector

import (
	"context"
	"errors"
	"fmt"
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

type stubStore struct {
	t                      *testing.T
	getChatMappingFn       func(context.Context, uuid.UUID, int64) (store.ChatMapping, bool, error)
	createChatMappingFn    func(context.Context, store.ChatMappingInput) (store.ChatMapping, error)
	deleteChatMappingFn    func(context.Context, uuid.UUID, int64) error
	clearChatBlockedFn     func(context.Context, uuid.UUID, int64) error
	markChatBlockedFn      func(context.Context, uuid.UUID, int64) error
	getInstallationStateFn func(context.Context, uuid.UUID) (int64, error)
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

func (s *stubStore) ClearChatBlocked(ctx context.Context, installationID uuid.UUID, chatID int64) error {
	if s.clearChatBlockedFn == nil {
		s.t.Fatalf("unexpected ClearChatBlocked")
	}
	return s.clearChatBlockedFn(ctx, installationID, chatID)
}

func (s *stubStore) MarkChatBlocked(ctx context.Context, installationID uuid.UUID, chatID int64) error {
	if s.markChatBlockedFn == nil {
		s.t.Fatalf("unexpected MarkChatBlocked")
	}
	return s.markChatBlockedFn(ctx, installationID, chatID)
}

func (s *stubStore) GetInstallationState(ctx context.Context, installationID uuid.UUID) (int64, error) {
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
		clearChatBlockedFn: func(context.Context, uuid.UUID, int64) error {
			t.Fatal("unexpected ClearChatBlocked")
			return nil
		},
		markChatBlockedFn: func(context.Context, uuid.UUID, int64) error {
			t.Fatal("unexpected MarkChatBlocked")
			return nil
		},
		getInstallationStateFn: func(context.Context, uuid.UUID) (int64, error) {
			t.Fatal("unexpected GetInstallationState")
			return 0, nil
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
		clearChatBlockedFn: func(context.Context, uuid.UUID, int64) error {
			return nil
		},
		markChatBlockedFn: func(context.Context, uuid.UUID, int64) error {
			t.Fatal("unexpected MarkChatBlocked")
			return nil
		},
		getInstallationStateFn: func(context.Context, uuid.UUID) (int64, error) {
			t.Fatal("unexpected GetInstallationState")
			return 0, nil
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
