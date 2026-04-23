package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
)

const (
	auditRetryDelay = time.Second
	auditRetryMax   = 3
)

const (
	auditEventPollingStarted       = "polling_started"
	auditEventPollingStopped       = "polling_stopped"
	auditEventConfigurationInvalid = "configuration_invalid"
	auditEventBotTokenRejected     = "bot_token_rejected"
	auditEventTelegramUnreachable  = "telegram_unreachable"
	auditEventTelegramRecovered    = "telegram_recovered"
	auditEventThreadDegraded       = "thread_degraded_rotated"
	auditEventOutboundFailed       = "outbound_delivery_failed"
	auditEventBotBlocked           = "bot_blocked_by_user"
	auditEventBotUnblocked         = "bot_unblocked_by_user"
)

type auditEvent struct {
	name           string
	message        string
	level          appsv1.InstallationAuditLogLevel
	idempotencyKey string
}

func (w *installationWorker) appendAudit(ctx context.Context, event auditEvent) {
	key := strings.TrimSpace(event.idempotencyKey)
	if key == "" {
		key = auditKey(event.name, w.installation.ID.String(), strconv.FormatInt(time.Now().UTC().Unix(), 10))
	}
	attempts := 0
	for {
		if ctx.Err() != nil {
			return
		}
		err := w.gateway.AppendInstallationAuditLogEntry(ctx, w.installation.ID.String(), event.message, event.level, key)
		if err == nil {
			return
		}
		attempts++
		if attempts >= auditRetryMax {
			log.Printf("connector: audit %s error: %v", event.name, err)
			if w.status != nil {
				w.status.RecordError(time.Now().UTC(), fmt.Sprintf("audit %s: %v", event.name, err))
			}
			return
		}
		if !sleepContext(ctx, auditRetryDelay) {
			return
		}
	}
}

func auditKey(event string, parts ...string) string {
	clean := make([]string, 0, len(parts)+1)
	clean = append(clean, event)
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		clean = append(clean, trimmed)
	}
	return strings.Join(clean, ":")
}

func auditKeyWithTime(event string, installationID uuid.UUID, timestamp time.Time, parts ...string) string {
	base := []string{installationID.String(), strconv.FormatInt(timestamp.UTC().Unix(), 10)}
	base = append(base, parts...)
	return auditKey(event, base...)
}

func auditKeyWithHash(event string, installationID uuid.UUID, value string) string {
	hash := sha256.Sum256([]byte(value))
	return auditKey(event, installationID.String(), hex.EncodeToString(hash[:]))
}
