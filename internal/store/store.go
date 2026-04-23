package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

type ChatMapping struct {
	ID             uuid.UUID
	InstallationID uuid.UUID
	TelegramChatID int64
	TelegramUserID int64
	ThreadID       uuid.UUID
	BlockedAt      *time.Time
	CreatedAt      time.Time
}

type ChatMappingInput struct {
	InstallationID uuid.UUID
	TelegramChatID int64
	TelegramUserID int64
	ThreadID       uuid.UUID
}

type InstallationState struct {
	LastUpdateID int64
	UpdatedAt    time.Time
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) GetChatMapping(ctx context.Context, installationID uuid.UUID, chatID int64) (ChatMapping, bool, error) {
	row := s.pool.QueryRow(ctx, `
        SELECT id, installation_id, telegram_chat_id, telegram_user_id, thread_id, blocked_at, created_at
        FROM chat_mappings
        WHERE installation_id = $1 AND telegram_chat_id = $2
    `, installationID, chatID)

	mapping, err := scanChatMapping(row)
	if err == nil {
		return mapping, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatMapping{}, false, nil
	}
	return ChatMapping{}, false, fmt.Errorf("get chat mapping: %w", err)
}

func (s *Store) GetChatMappingByThreadID(ctx context.Context, installationID, threadID uuid.UUID) (ChatMapping, bool, error) {
	row := s.pool.QueryRow(ctx, `
        SELECT id, installation_id, telegram_chat_id, telegram_user_id, thread_id, blocked_at, created_at
        FROM chat_mappings
        WHERE installation_id = $1 AND thread_id = $2
    `, installationID, threadID)

	mapping, err := scanChatMapping(row)
	if err == nil {
		return mapping, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatMapping{}, false, nil
	}
	return ChatMapping{}, false, fmt.Errorf("get chat mapping by thread: %w", err)
}

func (s *Store) CreateChatMapping(ctx context.Context, input ChatMappingInput) (ChatMapping, error) {
	row := s.pool.QueryRow(ctx, `
        INSERT INTO chat_mappings (installation_id, telegram_chat_id, telegram_user_id, thread_id)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (installation_id, telegram_chat_id)
        DO UPDATE SET telegram_user_id = EXCLUDED.telegram_user_id
        RETURNING id, installation_id, telegram_chat_id, telegram_user_id, thread_id, blocked_at, created_at
    `, input.InstallationID, input.TelegramChatID, input.TelegramUserID, input.ThreadID)

	mapping, err := scanChatMapping(row)
	if err != nil {
		return ChatMapping{}, fmt.Errorf("create chat mapping: %w", err)
	}
	return mapping, nil
}

func (s *Store) DeleteChatMapping(ctx context.Context, installationID uuid.UUID, chatID int64) error {
	if _, err := s.pool.Exec(ctx, `
        DELETE FROM chat_mappings
        WHERE installation_id = $1 AND telegram_chat_id = $2
    `, installationID, chatID); err != nil {
		return fmt.Errorf("delete chat mapping: %w", err)
	}
	return nil
}

func (s *Store) ClearChatBlocked(ctx context.Context, installationID uuid.UUID, chatID int64) (bool, error) {
	result, err := s.pool.Exec(ctx, `
        UPDATE chat_mappings
        SET blocked_at = NULL
        WHERE installation_id = $1 AND telegram_chat_id = $2 AND blocked_at IS NOT NULL
    `, installationID, chatID)
	if err != nil {
		return false, fmt.Errorf("clear chat blocked: %w", err)
	}
	return result.RowsAffected() > 0, nil
}

func (s *Store) MarkChatBlocked(ctx context.Context, installationID uuid.UUID, chatID int64) (bool, error) {
	result, err := s.pool.Exec(ctx, `
        UPDATE chat_mappings
        SET blocked_at = NOW()
        WHERE installation_id = $1 AND telegram_chat_id = $2 AND blocked_at IS NULL
    `, installationID, chatID)
	if err != nil {
		return false, fmt.Errorf("mark chat blocked: %w", err)
	}
	return result.RowsAffected() > 0, nil
}

func (s *Store) CountActiveChats(ctx context.Context, installationID uuid.UUID) (int64, error) {
	var count int64
	if err := s.pool.QueryRow(ctx, `
        SELECT COUNT(*)
        FROM chat_mappings
        WHERE installation_id = $1 AND blocked_at IS NULL
    `, installationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active chats: %w", err)
	}
	return count, nil
}

func (s *Store) CountBlockedChats(ctx context.Context, installationID uuid.UUID) (int64, error) {
	var count int64
	if err := s.pool.QueryRow(ctx, `
        SELECT COUNT(*)
        FROM chat_mappings
        WHERE installation_id = $1 AND blocked_at IS NOT NULL
    `, installationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count blocked chats: %w", err)
	}
	return count, nil
}

func (s *Store) GetInstallationState(ctx context.Context, installationID uuid.UUID) (InstallationState, error) {
	var state InstallationState
	err := s.pool.QueryRow(ctx, `
        SELECT last_update_id, updated_at
        FROM installation_state
        WHERE installation_id = $1
    `, installationID).Scan(&state.LastUpdateID, &state.UpdatedAt)
	if err == nil {
		return state, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return InstallationState{}, nil
	}
	return InstallationState{}, fmt.Errorf("get installation state: %w", err)
}

func (s *Store) UpsertInstallationState(ctx context.Context, installationID uuid.UUID, lastUpdateID int64) error {
	if _, err := s.pool.Exec(ctx, `
        INSERT INTO installation_state (installation_id, last_update_id, updated_at)
        VALUES ($1, $2, NOW())
        ON CONFLICT (installation_id)
        DO UPDATE SET last_update_id = EXCLUDED.last_update_id, updated_at = NOW()
    `, installationID, lastUpdateID); err != nil {
		return fmt.Errorf("upsert installation state: %w", err)
	}
	return nil
}

func scanChatMapping(row pgx.Row) (ChatMapping, error) {
	var mapping ChatMapping
	if err := row.Scan(
		&mapping.ID,
		&mapping.InstallationID,
		&mapping.TelegramChatID,
		&mapping.TelegramUserID,
		&mapping.ThreadID,
		&mapping.BlockedAt,
		&mapping.CreatedAt,
	); err != nil {
		return ChatMapping{}, err
	}
	return mapping, nil
}
