package connector

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/agynio/telegram-connector/internal/store"
	"github.com/agynio/telegram-connector/internal/telegram"
)

const (
	statusReportInterval       = 60 * time.Second
	statusWindow               = time.Hour
	telegramUnreachableTimeout = 60 * time.Second
)

type statusState string

const (
	statusHealthy       statusState = "Healthy"
	statusDegraded      statusState = "Degraded"
	statusMisconfigured statusState = "Misconfigured"
	statusStopped       statusState = "Stopped"
)

type statusError struct {
	message string
	at      time.Time
}

type rollingCounter struct {
	entries []time.Time
}

func (r *rollingCounter) add(ts time.Time) {
	r.entries = append(r.entries, ts)
	r.prune(ts)
}

func (r *rollingCounter) count(now time.Time) int {
	r.prune(now)
	return len(r.entries)
}

func (r *rollingCounter) prune(now time.Time) {
	cutoff := now.Add(-statusWindow)
	idx := 0
	for idx < len(r.entries) {
		if r.entries[idx].After(cutoff) {
			break
		}
		idx++
	}
	if idx > 0 {
		r.entries = append([]time.Time(nil), r.entries[idx:]...)
	}
}

type statusMetrics struct {
	State              statusState
	StartAt            time.Time
	LastUpdateAt       time.Time
	LastUpdateID       int64
	ActiveChats        int64
	BlockedChats       int64
	InboundMessages1h  int
	OutboundMessages1h int
	LastOutboundAt     time.Time
	LastError          *statusError
}

type statusSnapshot struct {
	state              statusState
	startAt            time.Time
	lastUpdateAt       time.Time
	lastUpdateID       int64
	inboundMessages1h  int
	outboundMessages1h int
	lastOutboundAt     time.Time
	lastError          *statusError
}

type installationStatus struct {
	installationID   uuid.UUID
	store            Store
	gateway          Gateway
	startAt          time.Time
	mu               sync.Mutex
	reportMu         sync.Mutex
	state            statusState
	lastUpdateAt     time.Time
	lastUpdateID     int64
	lastOutboundAt   time.Time
	inboundMessages  rollingCounter
	outboundMessages rollingCounter
	lastError        *statusError
	stopped          bool
	tokenRejected    bool
	pollingDegraded  bool
	outboundDegraded bool
}

func newInstallationStatus(installationID uuid.UUID, store Store, gateway Gateway, startAt time.Time, state store.InstallationState) *installationStatus {
	lastUpdateAt := startAt
	if !state.UpdatedAt.IsZero() {
		lastUpdateAt = state.UpdatedAt.UTC()
	}
	return &installationStatus{
		installationID: installationID,
		store:          store,
		gateway:        gateway,
		startAt:        startAt.UTC(),
		state:          statusHealthy,
		lastUpdateAt:   lastUpdateAt,
		lastUpdateID:   state.LastUpdateID,
		lastOutboundAt: startAt.UTC(),
	}
}

func (s *installationStatus) RecordPollSuccess(now time.Time, lastUpdateID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUpdateAt = now.UTC()
	if lastUpdateID > s.lastUpdateID {
		s.lastUpdateID = lastUpdateID
	}
}

func (s *installationStatus) RecordInbound(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inboundMessages.add(now.UTC())
}

func (s *installationStatus) RecordOutbound(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outboundMessages.add(now.UTC())
	s.lastOutboundAt = now.UTC()
}

func (s *installationStatus) RecordError(now time.Time, message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = &statusError{message: message, at: now.UTC()}
}

func (s *installationStatus) SetTokenRejected(rejected bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokenRejected == rejected {
		return false
	}
	s.tokenRejected = rejected
	return s.updateStateLocked()
}

func (s *installationStatus) TokenRejected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokenRejected
}

func (s *installationStatus) SetPollingDegraded(degraded bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pollingDegraded == degraded {
		return false
	}
	s.pollingDegraded = degraded
	return s.updateStateLocked()
}

func (s *installationStatus) SetOutboundDegraded(degraded bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outboundDegraded == degraded {
		return false
	}
	s.outboundDegraded = degraded
	return s.updateStateLocked()
}

func (s *installationStatus) SetStopped(stopped bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped == stopped {
		return false
	}
	s.stopped = stopped
	return s.updateStateLocked()
}

func (s *installationStatus) CurrentState() statusState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *installationStatus) Report(ctx context.Context) {
	s.reportMu.Lock()
	defer s.reportMu.Unlock()

	now := time.Now().UTC()
	activeChats, err := s.store.CountActiveChats(ctx, s.installationID)
	if err != nil {
		log.Printf("connector: count active chats error: %v", err)
		s.RecordError(now, fmt.Sprintf("count active chats: %v", err))
		activeChats = 0
	}
	blockedChats, err := s.store.CountBlockedChats(ctx, s.installationID)
	if err != nil {
		log.Printf("connector: count blocked chats error: %v", err)
		s.RecordError(now, fmt.Sprintf("count blocked chats: %v", err))
		blockedChats = 0
	}
	snapshot := s.snapshot(now)
	status := buildStatus(statusMetrics{
		State:              snapshot.state,
		StartAt:            snapshot.startAt,
		LastUpdateAt:       snapshot.lastUpdateAt,
		LastUpdateID:       snapshot.lastUpdateID,
		ActiveChats:        activeChats,
		BlockedChats:       blockedChats,
		InboundMessages1h:  snapshot.inboundMessages1h,
		OutboundMessages1h: snapshot.outboundMessages1h,
		LastOutboundAt:     snapshot.lastOutboundAt,
		LastError:          snapshot.lastError,
	})
	if err := s.gateway.ReportInstallationStatus(ctx, s.installationID.String(), status); err != nil {
		log.Printf("connector: report status error: %v", err)
		s.RecordError(now, fmt.Sprintf("report status: %v", err))
	}
}

func (s *installationStatus) snapshot(now time.Time) statusSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	lastError := s.lastError
	if lastError != nil && now.Sub(lastError.at) > statusWindow {
		lastError = nil
	}
	return statusSnapshot{
		state:              s.state,
		startAt:            s.startAt,
		lastUpdateAt:       s.lastUpdateAt,
		lastUpdateID:       s.lastUpdateID,
		inboundMessages1h:  s.inboundMessages.count(now),
		outboundMessages1h: s.outboundMessages.count(now),
		lastOutboundAt:     s.lastOutboundAt,
		lastError:          lastError,
	}
}

func (s *installationStatus) updateStateLocked() bool {
	state := statusHealthy
	switch {
	case s.stopped:
		state = statusStopped
	case s.tokenRejected:
		state = statusMisconfigured
	case s.pollingDegraded || s.outboundDegraded:
		state = statusDegraded
	}
	if state == s.state {
		return false
	}
	s.state = state
	return true
}

func buildStatus(metrics statusMetrics) string {
	headline := string(metrics.State)
	lines := []string{
		fmt.Sprintf("- status_since: %s", formatTimestamp(metrics.StartAt)),
		fmt.Sprintf("- last_update_at: %s", formatTimestamp(metrics.LastUpdateAt)),
		fmt.Sprintf("- last_update_id: %d", metrics.LastUpdateID),
		fmt.Sprintf("- active_chats: %d", metrics.ActiveChats),
		fmt.Sprintf("- blocked_chats: %d", metrics.BlockedChats),
		fmt.Sprintf("- inbound_messages_1h: %d", metrics.InboundMessages1h),
		fmt.Sprintf("- outbound_messages_1h: %d", metrics.OutboundMessages1h),
		fmt.Sprintf("- last_outbound_at: %s", formatTimestamp(metrics.LastOutboundAt)),
	}
	if metrics.LastError != nil {
		lines = append(lines, fmt.Sprintf("- last_error: %s at %s", metrics.LastError.message, formatTimestamp(metrics.LastError.at)))
	}
	return headline + "\n\n" + strings.Join(lines, "\n")
}

func formatTimestamp(ts time.Time) string {
	if ts.IsZero() {
		ts = time.Unix(0, 0).UTC()
	}
	return ts.UTC().Format(time.RFC3339)
}

func formatStatusError(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) {
		detail := strings.TrimSpace(apiErr.Description)
		if detail == "" {
			detail = "telegram api error"
		}
		return fmt.Sprintf("telegram status=%d code=%d: %s", apiErr.StatusCode, apiErr.ErrorCode, detail)
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		detail := strings.TrimSpace(connectErr.Message())
		if detail == "" {
			detail = "gateway error"
		}
		return fmt.Sprintf("gateway %s: %s", connectErr.Code(), detail)
	}
	return err.Error()
}
