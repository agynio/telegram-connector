CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE chat_mappings (
    id                UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    installation_id   UUID        NOT NULL,
    telegram_chat_id  BIGINT      NOT NULL,
    telegram_user_id  BIGINT      NOT NULL,
    thread_id         UUID        NOT NULL,
    blocked_at        TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX chat_mappings_installation_chat_idx ON chat_mappings (installation_id, telegram_chat_id);
CREATE INDEX chat_mappings_installation_thread_idx ON chat_mappings (installation_id, thread_id);
CREATE INDEX chat_mappings_installation_blocked_idx ON chat_mappings (installation_id, blocked_at) WHERE blocked_at IS NOT NULL;

CREATE TABLE installation_state (
    installation_id UUID        PRIMARY KEY,
    last_update_id  BIGINT      NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
