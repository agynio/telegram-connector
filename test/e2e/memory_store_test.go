//go:build e2e

package e2e

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/agynio/telegram-connector/internal/store"
)

type memoryStore struct {
	mu                 sync.Mutex
	chatByTelegram     map[int64]store.ChatMapping
	chatByThread       map[uuid.UUID]store.ChatMapping
	installationStates map[uuid.UUID]store.InstallationState
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		chatByTelegram:     make(map[int64]store.ChatMapping),
		chatByThread:       make(map[uuid.UUID]store.ChatMapping),
		installationStates: make(map[uuid.UUID]store.InstallationState),
	}
}

func (m *memoryStore) GetChatMapping(_ context.Context, _ uuid.UUID, telegramChatID int64) (store.ChatMapping, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mapping, ok := m.chatByTelegram[telegramChatID]
	return mapping, ok, nil
}

func (m *memoryStore) GetChatMappingByThreadID(_ context.Context, _ uuid.UUID, threadID uuid.UUID) (store.ChatMapping, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mapping, ok := m.chatByThread[threadID]
	return mapping, ok, nil
}

func (m *memoryStore) CreateChatMapping(_ context.Context, input store.ChatMappingInput) (store.ChatMapping, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mapping, ok := m.chatByTelegram[input.TelegramChatID]
	if !ok {
		mapping = store.ChatMapping{
			ID:             uuid.New(),
			InstallationID: input.InstallationID,
			TelegramChatID: input.TelegramChatID,
			TelegramUserID: input.TelegramUserID,
			ThreadID:       input.ThreadID,
			CreatedAt:      time.Now().UTC(),
		}
	} else {
		mapping.TelegramUserID = input.TelegramUserID
		mapping.ThreadID = input.ThreadID
	}
	m.chatByTelegram[input.TelegramChatID] = mapping
	m.chatByThread[input.ThreadID] = mapping
	return mapping, nil
}

func (m *memoryStore) DeleteChatMapping(_ context.Context, _ uuid.UUID, telegramChatID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mapping, ok := m.chatByTelegram[telegramChatID]
	if !ok {
		return nil
	}
	delete(m.chatByTelegram, telegramChatID)
	delete(m.chatByThread, mapping.ThreadID)
	return nil
}

func (m *memoryStore) MarkChatBlocked(_ context.Context, _ uuid.UUID, telegramChatID int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mapping, ok := m.chatByTelegram[telegramChatID]
	if !ok {
		return false, nil
	}
	if mapping.BlockedAt != nil {
		return false, nil
	}
	now := time.Now().UTC()
	mapping.BlockedAt = &now
	m.chatByTelegram[telegramChatID] = mapping
	m.chatByThread[mapping.ThreadID] = mapping
	return true, nil
}

func (m *memoryStore) ClearChatBlocked(_ context.Context, _ uuid.UUID, telegramChatID int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mapping, ok := m.chatByTelegram[telegramChatID]
	if !ok {
		return false, nil
	}
	if mapping.BlockedAt == nil {
		return false, nil
	}
	mapping.BlockedAt = nil
	m.chatByTelegram[telegramChatID] = mapping
	m.chatByThread[mapping.ThreadID] = mapping
	return true, nil
}

func (m *memoryStore) CountActiveChats(_ context.Context, installationID uuid.UUID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for _, mapping := range m.chatByTelegram {
		if mapping.InstallationID != installationID {
			continue
		}
		if mapping.BlockedAt == nil {
			count++
		}
	}
	return count, nil
}

func (m *memoryStore) CountBlockedChats(_ context.Context, installationID uuid.UUID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for _, mapping := range m.chatByTelegram {
		if mapping.InstallationID != installationID {
			continue
		}
		if mapping.BlockedAt != nil {
			count++
		}
	}
	return count, nil
}

func (m *memoryStore) UpsertInstallationState(_ context.Context, installationID uuid.UUID, lastUpdateID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.installationStates[installationID] = store.InstallationState{
		LastUpdateID: lastUpdateID,
		UpdatedAt:    time.Now().UTC(),
	}
	return nil
}

func (m *memoryStore) GetInstallationState(_ context.Context, installationID uuid.UUID) (store.InstallationState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.installationStates[installationID]
	if !ok {
		return store.InstallationState{}, nil
	}
	return state, nil
}
