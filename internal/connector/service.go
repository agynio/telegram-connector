package connector

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	notificationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/notifications/v1"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
	"github.com/agynio/telegram-connector/internal/store"
)

const reconcileInterval = 60 * time.Second

type Store interface {
	GetChatMapping(ctx context.Context, installationID uuid.UUID, chatID int64) (store.ChatMapping, bool, error)
	GetChatMappingByThreadID(ctx context.Context, installationID, threadID uuid.UUID) (store.ChatMapping, bool, error)
	CreateChatMapping(ctx context.Context, input store.ChatMappingInput) (store.ChatMapping, error)
	DeleteChatMapping(ctx context.Context, installationID uuid.UUID, chatID int64) error
	ClearChatBlocked(ctx context.Context, installationID uuid.UUID, chatID int64) (bool, error)
	MarkChatBlocked(ctx context.Context, installationID uuid.UUID, chatID int64) (bool, error)
	CountActiveChats(ctx context.Context, installationID uuid.UUID) (int64, error)
	CountBlockedChats(ctx context.Context, installationID uuid.UUID) (int64, error)
	GetInstallationState(ctx context.Context, installationID uuid.UUID) (store.InstallationState, error)
	UpsertInstallationState(ctx context.Context, installationID uuid.UUID, lastUpdateID int64) error
}

type Gateway interface {
	AppIdentityID() string
	ListInstallations(ctx context.Context, appID string) ([]*appsv1.Installation, error)
	CreateThread(ctx context.Context, organizationID string) (*threadsv1.Thread, error)
	AddParticipant(ctx context.Context, organizationID, threadID, participantID string) error
	SendMessage(ctx context.Context, threadID, body string, fileIDs []string) error
	GetUnackedMessages(ctx context.Context) ([]*threadsv1.Message, error)
	AckMessages(ctx context.Context, messageIDs []string) error
	Subscribe(ctx context.Context) (*connect.ServerStreamForClient[notificationsv1.SubscribeResponse], error)
	UploadFile(ctx context.Context, metadata *filesv1.UploadFileMetadata, payload []byte) (*filesv1.FileInfo, error)
	GetFileMetadata(ctx context.Context, fileID string) (*filesv1.FileInfo, error)
	GetFileContent(ctx context.Context, fileID string) ([]byte, error)
	ReportInstallationStatus(ctx context.Context, installationID, status string) error
	AppendInstallationAuditLogEntry(ctx context.Context, installationID, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey string) error
}

type Service struct {
	store           Store
	gateway         Gateway
	appID           string
	telegramBaseURL string
	pollTimeout     time.Duration
}

type Installation struct {
	ID             uuid.UUID
	OrganizationID uuid.UUID
	BotToken       string
	AgentID        uuid.UUID
}

type ServiceConfig struct {
	AppID           string
	TelegramBaseURL string
	PollTimeout     time.Duration
}

func NewService(store Store, gatewayClient Gateway, cfg ServiceConfig) *Service {
	return &Service{
		store:           store,
		gateway:         gatewayClient,
		appID:           cfg.AppID,
		telegramBaseURL: cfg.TelegramBaseURL,
		pollTimeout:     cfg.PollTimeout,
	}
}

func (s *Service) Run(ctx context.Context) error {
	manager := newInstallationManager(s.store, s.gateway, s.telegramBaseURL, s.pollTimeout)
	installations, err := s.listInstallations(ctx)
	if err != nil {
		return err
	}
	manager.Sync(ctx, installations)

	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			manager.StopAll()
			return nil
		case <-ticker.C:
			installations, err := s.listInstallations(ctx)
			if err != nil {
				log.Printf("connector: reconcile installations error: %v", err)
				continue
			}
			manager.Sync(ctx, installations)
		}
	}
}

func (s *Service) listInstallations(ctx context.Context) ([]Installation, error) {
	installations, err := s.gateway.ListInstallations(ctx, s.appID)
	if err != nil {
		return nil, err
	}
	resolved := make([]Installation, 0, len(installations))
	for _, installation := range installations {
		parsed, err := s.parseInstallation(ctx, installation)
		if err != nil {
			var configErr *installationConfigError
			if errors.As(err, &configErr) {
				s.reportConfigurationInvalid(ctx, configErr.installationID, configErr.message)
				continue
			}
			installationID := ""
			appID := ""
			if installation != nil {
				appID = installation.GetAppId()
				if installation.GetMeta() != nil {
					installationID = installation.GetMeta().GetId()
				}
			}
			log.Printf("connector: skip installation %s (app %s): %v", installationID, appID, err)
			continue
		}
		resolved = append(resolved, parsed)
	}
	return resolved, nil
}

func (s *Service) parseInstallation(ctx context.Context, installation *appsv1.Installation) (Installation, error) {
	if installation == nil || installation.GetMeta() == nil {
		return Installation{}, fmt.Errorf("installation missing metadata")
	}
	installationID, err := uuid.Parse(installation.GetMeta().GetId())
	if err != nil {
		return Installation{}, fmt.Errorf("invalid installation id: %w", err)
	}
	organizationID, err := uuid.Parse(installation.GetOrganizationId())
	if err != nil {
		return Installation{}, fmt.Errorf("invalid organization id: %w", err)
	}
	botToken, agentID, err := parseInstallationConfig(installation.GetConfiguration())
	if err != nil {
		return Installation{}, &installationConfigError{installationID: installationID, message: err.Error()}
	}
	return Installation{
		ID:             installationID,
		OrganizationID: organizationID,
		BotToken:       botToken,
		AgentID:        agentID,
	}, nil
}

func (s *Service) reportConfigurationInvalid(ctx context.Context, installationID uuid.UUID, message string) {
	now := time.Now().UTC()
	state, err := s.store.GetInstallationState(ctx, installationID)
	if err != nil {
		log.Printf("connector: get installation state for %s error: %v", installationID, err)
	}
	lastUpdateAt := state.UpdatedAt
	if lastUpdateAt.IsZero() {
		lastUpdateAt = now
	}
	activeChats, err := s.store.CountActiveChats(ctx, installationID)
	if err != nil {
		log.Printf("connector: count active chats for %s error: %v", installationID, err)
		activeChats = 0
	}
	blockedChats, err := s.store.CountBlockedChats(ctx, installationID)
	if err != nil {
		log.Printf("connector: count blocked chats for %s error: %v", installationID, err)
		blockedChats = 0
	}
	status := buildStatus(statusMetrics{
		State:              statusMisconfigured,
		StartAt:            now,
		LastUpdateAt:       lastUpdateAt,
		LastUpdateID:       state.LastUpdateID,
		ActiveChats:        activeChats,
		BlockedChats:       blockedChats,
		InboundMessages1h:  0,
		OutboundMessages1h: 0,
		LastOutboundAt:     lastUpdateAt,
		LastError:          &statusError{message: message, at: now},
	})
	if err := s.gateway.ReportInstallationStatus(ctx, installationID.String(), status); err != nil {
		log.Printf("connector: report status for %s error: %v", installationID, err)
	}
	auditMessage := fmt.Sprintf("%s: %s", auditEventConfigurationInvalid, message)
	if err := s.gateway.AppendInstallationAuditLogEntry(ctx, installationID.String(), auditMessage, appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_ERROR, auditKeyWithHash(auditEventConfigurationInvalid, installationID, message)); err != nil {
		log.Printf("connector: append audit log for %s error: %v", installationID, err)
	}
}

type installationManager struct {
	store           Store
	gateway         Gateway
	telegramBaseURL string
	pollTimeout     time.Duration
	mu              sync.Mutex
	workers         map[uuid.UUID]*installationWorker
}

func newInstallationManager(store Store, gatewayClient Gateway, telegramBaseURL string, pollTimeout time.Duration) *installationManager {
	return &installationManager{
		store:           store,
		gateway:         gatewayClient,
		telegramBaseURL: telegramBaseURL,
		pollTimeout:     pollTimeout,
		workers:         make(map[uuid.UUID]*installationWorker),
	}
}

func (m *installationManager) Sync(ctx context.Context, installations []Installation) {
	desired := make(map[uuid.UUID]Installation, len(installations))
	for _, installation := range installations {
		desired[installation.ID] = installation
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, worker := range m.workers {
		if _, ok := desired[id]; !ok {
			worker.Stop(stopReasonRemoved)
			delete(m.workers, id)
		}
	}

	for id, installation := range desired {
		worker, ok := m.workers[id]
		if ok && worker.Installation().Equal(installation) {
			continue
		}
		if ok {
			worker.Stop(stopReasonShutdown)
			delete(m.workers, id)
		}
		newWorker := newInstallationWorker(installation, m.store, m.gateway, m.telegramBaseURL, m.pollTimeout)
		newWorker.Start(ctx)
		m.workers[id] = newWorker
	}
}

func (m *installationManager) StopAll() {
	m.mu.Lock()
	workers := make([]*installationWorker, 0, len(m.workers))
	for _, worker := range m.workers {
		workers = append(workers, worker)
	}
	m.workers = make(map[uuid.UUID]*installationWorker)
	m.mu.Unlock()

	for _, worker := range workers {
		worker.Stop(stopReasonShutdown)
	}
}

func (i Installation) Equal(other Installation) bool {
	return i.ID == other.ID &&
		i.OrganizationID == other.OrganizationID &&
		i.BotToken == other.BotToken &&
		i.AgentID == other.AgentID
}
