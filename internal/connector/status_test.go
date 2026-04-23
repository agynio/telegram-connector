package connector

import (
	"strings"
	"testing"
	"time"
)

func TestBuildStatusIncludesMetrics(t *testing.T) {
	timestamp := time.Date(2026, 4, 23, 9, 4, 9, 0, time.UTC)
	status := buildStatus(statusMetrics{
		State:              statusHealthy,
		StartAt:            timestamp,
		LastUpdateAt:       timestamp,
		LastUpdateID:       184522,
		ActiveChats:        47,
		BlockedChats:       2,
		InboundMessages1h:  120,
		OutboundMessages1h: 98,
		LastOutboundAt:     timestamp,
		LastError:          &statusError{message: "telegram status=500", at: timestamp},
	})

	checks := []string{
		"**Healthy**",
		"polling since " + timestamp.Format(time.RFC3339),
		"- last_update_at: " + timestamp.Format(time.RFC3339),
		"- last_update_id: 184522",
		"- active_chats: 47",
		"- blocked_chats: 2",
		"- inbound_messages_1h: 120",
		"- outbound_messages_1h: 98",
		"- last_outbound_at: " + timestamp.Format(time.RFC3339),
		"- last_error: telegram status=500 at " + timestamp.Format(time.RFC3339),
	}
	for _, check := range checks {
		if !strings.Contains(status, check) {
			t.Fatalf("expected status to contain %q, got %q", check, status)
		}
	}
}
