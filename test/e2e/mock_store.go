//go:build e2e

package e2e

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/agynio/telegram-connector/internal/store"
)

type mockStore struct {
	mu                 sync.Mutex
	chatMappings       map[string]store.ChatMapping
	threadMappings     map[string]store.ChatMapping
	installationStates map[uuid.UUID]int64
}

func newMockStore() *mockStore {
	return &mockStore{
		chatMappings:       make(map[string]store.ChatMapping),
		threadMappings:     make(map[string]store.ChatMapping),
		installationStates: make(map[uuid.UUID]int64),
	}
}

func (m *mockStore) GetChatMapping(_ context.Context, installationID uuid.UUID, chatID int64) (store.ChatMapping, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mapping, ok := m.chatMappings[chatKey(installationID, chatID)]
	return mapping, ok, nil
}

func (m *mockStore) GetChatMappingByThreadID(_ context.Context, installationID, threadID uuid.UUID) (store.ChatMapping, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mapping, ok := m.threadMappings[threadKey(installationID, threadID)]
	return mapping, ok, nil
}

func (m *mockStore) CreateChatMapping(_ context.Context, input store.ChatMappingInput) (store.ChatMapping, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := chatKey(input.InstallationID, input.TelegramChatID)
	if existing, ok := m.chatMappings[key]; ok {
		existing.TelegramUserID = input.TelegramUserID
		m.chatMappings[key] = existing
		m.threadMappings[threadKey(input.InstallationID, existing.ThreadID)] = existing
		return existing, nil
	}
	mapping := store.ChatMapping{
		ID:             uuid.New(),
		InstallationID: input.InstallationID,
		TelegramChatID: input.TelegramChatID,
		TelegramUserID: input.TelegramUserID,
		ThreadID:       input.ThreadID,
		CreatedAt:      time.Now(),
	}
	m.chatMappings[key] = mapping
	m.threadMappings[threadKey(input.InstallationID, input.ThreadID)] = mapping
	return mapping, nil
}

func (m *mockStore) ClearChatBlocked(_ context.Context, installationID uuid.UUID, chatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := chatKey(installationID, chatID)
	mapping, ok := m.chatMappings[key]
	if !ok {
		return nil
	}
	mapping.BlockedAt = nil
	m.chatMappings[key] = mapping
	m.threadMappings[threadKey(installationID, mapping.ThreadID)] = mapping
	return nil
}

func (m *mockStore) MarkChatBlocked(_ context.Context, installationID uuid.UUID, chatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := chatKey(installationID, chatID)
	mapping, ok := m.chatMappings[key]
	if !ok {
		return nil
	}
	now := time.Now()
	mapping.BlockedAt = &now
	m.chatMappings[key] = mapping
	m.threadMappings[threadKey(installationID, mapping.ThreadID)] = mapping
	return nil
}

func (m *mockStore) GetInstallationState(_ context.Context, installationID uuid.UUID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installationStates[installationID], nil
}

func (m *mockStore) UpsertInstallationState(_ context.Context, installationID uuid.UUID, lastUpdateID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.installationStates[installationID] = lastUpdateID
	return nil
}

func (m *mockStore) getMappingByChat(installationID uuid.UUID, chatID int64) (store.ChatMapping, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mapping, ok := m.chatMappings[chatKey(installationID, chatID)]
	return mapping, ok
}

func chatKey(installationID uuid.UUID, chatID int64) string {
	return installationID.String() + ":" + strconv.FormatInt(chatID, 10)
}

func threadKey(installationID, threadID uuid.UUID) string {
	return installationID.String() + ":" + threadID.String()
}
