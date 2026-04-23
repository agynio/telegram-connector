package connector

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	"github.com/agynio/telegram-connector/internal/store"
)

func TestWorkerRunEmitsPollingStartedAudit(t *testing.T) {
	installationID := uuid.New()
	organizationID := uuid.New()
	agentID := uuid.New()
	statusCalls := 0
	auditCalls := 0

	storeStub := &stubStore{
		t: t,
		getInstallationStateFn: func(context.Context, uuid.UUID) (store.InstallationState, error) {
			return store.InstallationState{}, nil
		},
		countActiveChatsFn: func(context.Context, uuid.UUID) (int64, error) {
			return 0, nil
		},
		countBlockedChatsFn: func(context.Context, uuid.UUID) (int64, error) {
			return 0, nil
		},
	}

	gatewayStub := &stubGateway{
		t: t,
		reportStatusFn: func(_ context.Context, installationArg, status string) error {
			statusCalls++
			if installationArg != installationID.String() {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if !strings.HasPrefix(status, "Healthy\n\n") {
				t.Fatalf("expected healthy headline, got %q", status)
			}
			return nil
		},
		appendAuditFn: func(_ context.Context, installationArg, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey string) error {
			auditCalls++
			if installationArg != installationID.String() {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if level != appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_INFO {
				t.Fatalf("expected info level, got %v", level)
			}
			if !strings.Contains(message, auditEventPollingStarted) {
				t.Fatalf("expected polling_started message, got %q", message)
			}
			prefix := auditEventPollingStarted + ":" + installationID.String() + ":"
			if !strings.HasPrefix(idempotencyKey, prefix) {
				t.Fatalf("expected idempotency key prefix %q, got %q", prefix, idempotencyKey)
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
	worker.runInboundFn = func(context.Context, store.InstallationState) error {
		return nil
	}
	worker.runOutboundFn = func(context.Context) error {
		return nil
	}

	worker.run(context.Background())

	if statusCalls != 1 {
		t.Fatalf("expected 1 status report, got %d", statusCalls)
	}
	if auditCalls != 1 {
		t.Fatalf("expected 1 audit entry, got %d", auditCalls)
	}
}

func TestReportStopClearsStatusOnRemoval(t *testing.T) {
	installationID := uuid.New()
	organizationID := uuid.New()
	agentID := uuid.New()
	statusCalls := 0
	auditCalls := 0

	gatewayStub := &stubGateway{
		t: t,
		reportStatusFn: func(_ context.Context, installationArg, status string) error {
			statusCalls++
			if installationArg != installationID.String() {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if status != "" {
				t.Fatalf("expected cleared status, got %q", status)
			}
			return nil
		},
		appendAuditFn: func(_ context.Context, installationArg, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey string) error {
			auditCalls++
			if installationArg != installationID.String() {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if !strings.Contains(message, auditEventPollingStopped) {
				t.Fatalf("expected polling_stopped message, got %q", message)
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
	}, &stubStore{t: t}, gatewayStub, "http://telegram.test", time.Second)

	worker.reportStop(stopReasonRemoved)

	if statusCalls != 1 {
		t.Fatalf("expected 1 status clear, got %d", statusCalls)
	}
	if auditCalls != 1 {
		t.Fatalf("expected 1 audit entry, got %d", auditCalls)
	}
}

func TestReportStopMisconfiguredDoesNotClearStatus(t *testing.T) {
	installationID := uuid.New()
	organizationID := uuid.New()
	agentID := uuid.New()
	statusCalls := 0
	auditCalls := 0

	gatewayStub := &stubGateway{
		t: t,
		reportStatusFn: func(context.Context, string, string) error {
			statusCalls++
			return nil
		},
		appendAuditFn: func(_ context.Context, installationArg, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey string) error {
			auditCalls++
			if installationArg != installationID.String() {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if !strings.Contains(message, auditEventPollingStopped) {
				t.Fatalf("expected polling_stopped message, got %q", message)
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
	}, &stubStore{t: t}, gatewayStub, "http://telegram.test", time.Second)

	worker.reportStop(stopReasonMisconfigured)

	if statusCalls != 0 {
		t.Fatalf("expected no status clear, got %d", statusCalls)
	}
	if auditCalls != 1 {
		t.Fatalf("expected 1 audit entry, got %d", auditCalls)
	}
}

func TestHandleTokenRejectedEmitsAuditOnce(t *testing.T) {
	installationID := uuid.New()
	organizationID := uuid.New()
	agentID := uuid.New()
	now := time.Date(2026, 4, 23, 10, 5, 0, 0, time.UTC)
	statusCalls := 0
	auditCalls := 0

	storeStub := &stubStore{
		t: t,
		countActiveChatsFn: func(context.Context, uuid.UUID) (int64, error) {
			return 0, nil
		},
		countBlockedChatsFn: func(context.Context, uuid.UUID) (int64, error) {
			return 0, nil
		},
	}

	gatewayStub := &stubGateway{
		t: t,
		reportStatusFn: func(_ context.Context, installationArg, status string) error {
			statusCalls++
			if installationArg != installationID.String() {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if !strings.HasPrefix(status, "Misconfigured\n\n") {
				t.Fatalf("expected misconfigured headline, got %q", status)
			}
			return nil
		},
		appendAuditFn: func(_ context.Context, installationArg, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey string) error {
			auditCalls++
			if installationArg != installationID.String() {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if !strings.Contains(message, auditEventBotTokenRejected) {
				t.Fatalf("expected bot_token_rejected message, got %q", message)
			}
			expected := auditKeyWithTime(auditEventBotTokenRejected, installationID, now)
			if idempotencyKey != expected {
				t.Fatalf("expected idempotency key %q, got %q", expected, idempotencyKey)
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
	worker.status = newInstallationStatus(installationID, storeStub, gatewayStub, now, store.InstallationState{})

	worker.handleTokenRejected(context.Background(), now)
	worker.handleTokenRejected(context.Background(), now.Add(time.Minute))

	if statusCalls != 1 {
		t.Fatalf("expected 1 status report, got %d", statusCalls)
	}
	if auditCalls != 1 {
		t.Fatalf("expected 1 audit entry, got %d", auditCalls)
	}
	if worker.status.CurrentState() != statusMisconfigured {
		t.Fatalf("expected misconfigured state, got %s", worker.status.CurrentState())
	}
}

func TestHandlePollErrorUnreachableAndRecovered(t *testing.T) {
	installationID := uuid.New()
	organizationID := uuid.New()
	agentID := uuid.New()
	start := time.Date(2026, 4, 23, 11, 0, 0, 0, time.UTC)
	auditEvents := make([]string, 0, 2)
	second := time.Time{}

	storeStub := &stubStore{
		t: t,
		countActiveChatsFn: func(context.Context, uuid.UUID) (int64, error) {
			return 0, nil
		},
		countBlockedChatsFn: func(context.Context, uuid.UUID) (int64, error) {
			return 0, nil
		},
	}

	gatewayStub := &stubGateway{
		t: t,
		reportStatusFn: func(context.Context, string, string) error {
			return nil
		},
		appendAuditFn: func(_ context.Context, installationArg, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey string) error {
			if installationArg != installationID.String() {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if strings.TrimSpace(idempotencyKey) == "" {
				t.Fatal("expected idempotency key")
			}
			event := strings.SplitN(message, ":", 2)[0]
			auditEvents = append(auditEvents, event)
			if event == auditEventTelegramUnreachable {
				expected := auditKeyWithTime(auditEventTelegramUnreachable, installationID, start)
				if idempotencyKey != expected {
					t.Fatalf("expected idempotency key %q, got %q", expected, idempotencyKey)
				}
			}
			if event == auditEventTelegramRecovered {
				expected := auditKeyWithTime(auditEventTelegramRecovered, installationID, second.Add(time.Second))
				if idempotencyKey != expected {
					t.Fatalf("expected idempotency key %q, got %q", expected, idempotencyKey)
				}
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
	worker.status = newInstallationStatus(installationID, storeStub, gatewayStub, start, store.InstallationState{})

	worker.handlePollError(context.Background(), start, errTest("getUpdates failed"))
	if len(auditEvents) != 0 {
		t.Fatalf("expected no audits yet, got %v", auditEvents)
	}
	second = start.Add(telegramUnreachableTimeout + time.Second)
	worker.handlePollError(context.Background(), second, errTest("getUpdates failed"))

	if len(auditEvents) != 1 || auditEvents[0] != auditEventTelegramUnreachable {
		t.Fatalf("expected unreachable audit, got %v", auditEvents)
	}

	worker.handlePollSuccess(context.Background(), second.Add(time.Second))

	if len(auditEvents) != 2 || auditEvents[1] != auditEventTelegramRecovered {
		t.Fatalf("expected recovered audit, got %v", auditEvents)
	}
	if worker.telegramUnreachable {
		t.Fatal("expected telegramUnreachable reset")
	}
}

type errTest string

func (e errTest) Error() string {
	return string(e)
}
