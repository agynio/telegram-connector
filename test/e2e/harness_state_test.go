//go:build e2e

package e2e

import (
	"sync"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	notificationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/notifications/v1"
	organizationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/organizations/v1"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
)

type testState struct {
	mu                 sync.Mutex
	organizations      map[string]*organizationsv1.Organization
	apps               map[string]*appsv1.App
	installations      map[string]*appsv1.Installation
	installationStatus map[string]string
	auditLogs          map[string][]*appsv1.InstallationAuditLogEntry
	threads            map[string]*threadState
	messages           map[string]*messageState
	files              map[string]*fileState
	subscribers        map[int]chan *notificationsv1.SubscribeResponse
	nextSubscriberID   int
}

type threadState struct {
	thread       *threadsv1.Thread
	participants map[string]*threadsv1.Participant
	messages     []*messageState
}

type messageState struct {
	message *threadsv1.Message
	acked   map[string]struct{}
}

type fileState struct {
	info *filesv1.FileInfo
	data []byte
}

func newTestState() *testState {
	return &testState{
		organizations:      make(map[string]*organizationsv1.Organization),
		apps:               make(map[string]*appsv1.App),
		installations:      make(map[string]*appsv1.Installation),
		installationStatus: make(map[string]string),
		auditLogs:          make(map[string][]*appsv1.InstallationAuditLogEntry),
		threads:            make(map[string]*threadState),
		messages:           make(map[string]*messageState),
		files:              make(map[string]*fileState),
		subscribers:        make(map[int]chan *notificationsv1.SubscribeResponse),
	}
}

func (s *testState) createOrganization(name string) *organizationsv1.Organization {
	s.mu.Lock()
	defer s.mu.Unlock()
	org := &organizationsv1.Organization{
		Id:        uuid.NewString(),
		Name:      name,
		CreatedAt: timestamppb.Now(),
		UpdatedAt: timestamppb.Now(),
	}
	s.organizations[org.GetId()] = org
	return cloneOrganization(org)
}

func (s *testState) deleteOrganization(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.organizations, id)
}

func (s *testState) createApp(req *appsv1.CreateAppRequest) (*appsv1.App, string, error) {
	if req.GetOrganizationId() == "" {
		return nil, "", status.Error(codes.InvalidArgument, "organization_id required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app := &appsv1.App{
		Meta: &appsv1.EntityMeta{
			Id:        uuid.NewString(),
			CreatedAt: timestamppb.Now(),
			UpdatedAt: timestamppb.Now(),
		},
		Slug:           req.GetSlug(),
		Name:           req.GetName(),
		Description:    req.GetDescription(),
		OrganizationId: req.GetOrganizationId(),
		Visibility:     req.GetVisibility(),
		Permissions:    req.GetPermissions(),
		IdentityId:     uuid.NewString(),
	}
	s.apps[app.GetMeta().GetId()] = app
	serviceToken := "svc-" + uuid.NewString()
	return cloneApp(app), serviceToken, nil
}

func (s *testState) deleteApp(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.apps, id)
	for installationID, installation := range s.installations {
		if installation.GetAppId() == id {
			delete(s.installations, installationID)
		}
	}
}

func (s *testState) installApp(appID, organizationID string, config *structpb.Struct) (*appsv1.Installation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apps[appID]; !ok {
		return nil, status.Error(codes.NotFound, "app not found")
	}
	for _, installation := range s.installations {
		if installation.GetAppId() == appID && installation.GetOrganizationId() == organizationID {
			return nil, status.Error(codes.AlreadyExists, "installation exists")
		}
	}
	installation := &appsv1.Installation{
		Meta: &appsv1.EntityMeta{
			Id:        uuid.NewString(),
			CreatedAt: timestamppb.Now(),
			UpdatedAt: timestamppb.Now(),
		},
		AppId:          appID,
		OrganizationId: organizationID,
		Configuration:  config,
	}
	s.installations[installation.GetMeta().GetId()] = installation
	return cloneInstallation(installation), nil
}

func (s *testState) updateInstallation(installationID string, config *structpb.Struct) (*appsv1.Installation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	installation, ok := s.installations[installationID]
	if !ok {
		return nil, status.Error(codes.NotFound, "installation not found")
	}
	installation.Configuration = config
	if installation.Meta != nil {
		installation.Meta.UpdatedAt = timestamppb.Now()
	}
	s.installations[installationID] = installation
	return cloneInstallation(installation), nil
}

func (s *testState) uninstallApp(installationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.installations[installationID]; !ok {
		return status.Error(codes.NotFound, "installation not found")
	}
	delete(s.installations, installationID)
	return nil
}

func (s *testState) listInstallations(appID string) []*appsv1.Installation {
	s.mu.Lock()
	defer s.mu.Unlock()
	installations := make([]*appsv1.Installation, 0, len(s.installations))
	for _, installation := range s.installations {
		if appID != "" && installation.GetAppId() != appID {
			continue
		}
		installations = append(installations, cloneInstallation(installation))
	}
	return installations
}

func (s *testState) reportInstallationStatus(installationID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	installation, ok := s.installations[installationID]
	if !ok {
		return statusError(codes.NotFound, "installation not found")
	}
	if status == "" {
		installation.Status = nil
	} else {
		installation.Status = &status
	}
	s.installations[installationID] = installation
	s.installationStatus[installationID] = status
	return nil
}

func (s *testState) appendAudit(installationID, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey *string) *appsv1.InstallationAuditLogEntry {
	entries := s.auditLogs[installationID]
	if idempotencyKey != nil {
		for _, entry := range entries {
			if entry.GetIdempotencyKey() == *idempotencyKey {
				return cloneAudit(entry)
			}
		}
	}
	entry := &appsv1.InstallationAuditLogEntry{
		Id:             uuid.NewString(),
		InstallationId: installationID,
		Message:        message,
		Level:          level,
		IdempotencyKey: idempotencyKey,
		CreatedAt:      timestamppb.Now(),
	}
	s.auditLogs[installationID] = append(entries, entry)
	return cloneAudit(entry)
}

func (s *testState) createThread(organizationID string) *threadsv1.Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread := &threadsv1.Thread{
		Id:             uuid.NewString(),
		OrganizationId: organizationID,
		Status:         threadsv1.ThreadStatus_THREAD_STATUS_ACTIVE,
		CreatedAt:      timestamppb.Now(),
		UpdatedAt:      timestamppb.Now(),
	}
	s.threads[thread.Id] = &threadState{
		thread:       thread,
		participants: make(map[string]*threadsv1.Participant),
		messages:     make([]*messageState, 0),
	}
	return cloneThread(thread)
}

func (s *testState) addParticipant(threadID, participantID string, passive bool) (*threadsv1.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	threadState, ok := s.threads[threadID]
	if !ok {
		return nil, status.Error(codes.NotFound, "thread not found")
	}
	participant := &threadsv1.Participant{
		Id:       participantID,
		JoinedAt: timestamppb.Now(),
		Passive:  passive,
	}
	threadState.participants[participantID] = participant
	threadState.thread.Participants = participantsFromMap(threadState.participants)
	threadState.thread.UpdatedAt = timestamppb.Now()
	return cloneThread(threadState.thread), nil
}

func (s *testState) sendMessage(threadID, senderID, body string, fileIDs []string) (*threadsv1.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	threadState, ok := s.threads[threadID]
	if !ok {
		return nil, status.Error(codes.NotFound, "thread not found")
	}
	message := &threadsv1.Message{
		Id:        uuid.NewString(),
		ThreadId:  threadID,
		SenderId:  senderID,
		Body:      body,
		FileIds:   append([]string(nil), fileIDs...),
		CreatedAt: timestamppb.Now(),
	}
	messageState := &messageState{message: message, acked: make(map[string]struct{})}
	threadState.messages = append(threadState.messages, messageState)
	s.messages[message.Id] = messageState
	threadState.thread.MessageCount = int32(len(threadState.messages))
	threadState.thread.UpdatedAt = timestamppb.Now()
	return cloneMessage(message), nil
}

func (s *testState) listThreads(participantID string) []*threadsv1.Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	threads := make([]*threadsv1.Thread, 0)
	for _, threadState := range s.threads {
		if participantID != "" {
			if _, ok := threadState.participants[participantID]; !ok {
				continue
			}
		}
		threads = append(threads, cloneThread(threadState.thread))
	}
	return threads
}

func (s *testState) listMessages(threadID string) ([]*threadsv1.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	threadState, ok := s.threads[threadID]
	if !ok {
		return nil, status.Error(codes.NotFound, "thread not found")
	}
	messages := make([]*threadsv1.Message, 0, len(threadState.messages))
	for _, messageState := range threadState.messages {
		messages = append(messages, cloneMessage(messageState.message))
	}
	return messages, nil
}

func (s *testState) listUnackedMessages(participantID string) []*threadsv1.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	messages := make([]*threadsv1.Message, 0)
	for _, messageState := range s.messages {
		if _, ok := messageState.acked[participantID]; ok {
			continue
		}
		messages = append(messages, cloneMessage(messageState.message))
	}
	return messages
}

func (s *testState) ackMessages(participantID string, messageIDs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, messageID := range messageIDs {
		messageState, ok := s.messages[messageID]
		if !ok {
			continue
		}
		messageState.acked[participantID] = struct{}{}
	}
}

func (s *testState) addFile(metadata *filesv1.UploadFileMetadata, data []byte) *filesv1.FileInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := &filesv1.FileInfo{
		Id:          uuid.NewString(),
		Filename:    metadata.GetFilename(),
		ContentType: metadata.GetContentType(),
		SizeBytes:   int64(len(data)),
		CreatedAt:   timestamppb.Now(),
	}
	s.files[info.Id] = &fileState{info: info, data: append([]byte(nil), data...)}
	return cloneFile(info)
}

func (s *testState) getFile(fileID string) (*filesv1.FileInfo, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fileState, ok := s.files[fileID]
	if !ok {
		return nil, nil, status.Error(codes.NotFound, "file not found")
	}
	return cloneFile(fileState.info), append([]byte(nil), fileState.data...), nil
}

func (s *testState) subscribeNotifications() (int, chan *notificationsv1.SubscribeResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSubscriberID++
	id := s.nextSubscriberID
	ch := make(chan *notificationsv1.SubscribeResponse, 10)
	s.subscribers[id] = ch
	return id, ch
}

func (s *testState) unsubscribeNotifications(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.subscribers[id]
	if ok {
		close(ch)
		delete(s.subscribers, id)
	}
}

func (s *testState) publishNotification(envelope *notificationsv1.NotificationEnvelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subscribers {
		ch <- &notificationsv1.SubscribeResponse{Envelope: envelope}
	}
}

func cloneApp(app *appsv1.App) *appsv1.App {
	return proto.Clone(app).(*appsv1.App)
}

func cloneInstallation(installation *appsv1.Installation) *appsv1.Installation {
	return proto.Clone(installation).(*appsv1.Installation)
}

func cloneOrganization(org *organizationsv1.Organization) *organizationsv1.Organization {
	return proto.Clone(org).(*organizationsv1.Organization)
}

func cloneThread(thread *threadsv1.Thread) *threadsv1.Thread {
	return proto.Clone(thread).(*threadsv1.Thread)
}

func cloneMessage(message *threadsv1.Message) *threadsv1.Message {
	return proto.Clone(message).(*threadsv1.Message)
}

func cloneFile(file *filesv1.FileInfo) *filesv1.FileInfo {
	return proto.Clone(file).(*filesv1.FileInfo)
}

func cloneAudit(entry *appsv1.InstallationAuditLogEntry) *appsv1.InstallationAuditLogEntry {
	return proto.Clone(entry).(*appsv1.InstallationAuditLogEntry)
}

func participantsFromMap(participants map[string]*threadsv1.Participant) []*threadsv1.Participant {
	result := make([]*threadsv1.Participant, 0, len(participants))
	for _, participant := range participants {
		result = append(result, proto.Clone(participant).(*threadsv1.Participant))
	}
	return result
}

func statusError(code codes.Code, message string) error {
	return status.Error(code, message)
}
