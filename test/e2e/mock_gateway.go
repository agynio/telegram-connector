//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	"github.com/agynio/telegram-connector/.gen/go/agynio/api/gateway/v1/gatewayv1connect"
	notificationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/notifications/v1"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
)

type mockGateway struct {
	gatewayv1connect.UnimplementedAppsGatewayHandler
	gatewayv1connect.UnimplementedThreadsGatewayHandler
	gatewayv1connect.UnimplementedFilesGatewayHandler
	gatewayv1connect.UnimplementedNotificationsGatewayHandler
	mu            sync.Mutex
	installations []*appsv1.Installation
	threads       map[string]*threadsv1.Thread
	sentMessages  []*threadsv1.Message
	unacked       map[string]*threadsv1.Message
	files         map[string]fileRecord
	notifications *mockNotifications
}

type fileRecord struct {
	info *filesv1.FileInfo
	data []byte
}

type mockNotifications struct {
	mu          sync.Mutex
	subscribers map[chan *notificationsv1.SubscribeResponse]struct{}
}

func newMockGateway(installations []*appsv1.Installation) *mockGateway {
	return &mockGateway{
		installations: installations,
		threads:       make(map[string]*threadsv1.Thread),
		unacked:       make(map[string]*threadsv1.Message),
		files:         make(map[string]fileRecord),
		notifications: &mockNotifications{subscribers: make(map[chan *notificationsv1.SubscribeResponse]struct{})},
	}
}

func (m *mockGateway) ListInstallations(_ context.Context, _ *connect.Request[appsv1.ListInstallationsRequest]) (*connect.Response[appsv1.ListInstallationsResponse], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	installations := make([]*appsv1.Installation, len(m.installations))
	copy(installations, m.installations)
	return connect.NewResponse(&appsv1.ListInstallationsResponse{Installations: installations}), nil
}

func (m *mockGateway) CreateThread(_ context.Context, _ *connect.Request[threadsv1.CreateThreadRequest]) (*connect.Response[threadsv1.CreateThreadResponse], error) {
	threadID := uuid.New().String()
	thread := &threadsv1.Thread{Id: threadID}
	m.mu.Lock()
	m.threads[threadID] = thread
	m.mu.Unlock()
	return connect.NewResponse(&threadsv1.CreateThreadResponse{Thread: thread}), nil
}

func (m *mockGateway) AddParticipant(_ context.Context, req *connect.Request[threadsv1.AddParticipantRequest]) (*connect.Response[threadsv1.AddParticipantResponse], error) {
	m.mu.Lock()
	thread := m.threads[req.Msg.GetThreadId()]
	m.mu.Unlock()
	return connect.NewResponse(&threadsv1.AddParticipantResponse{Thread: thread}), nil
}

func (m *mockGateway) SendMessage(_ context.Context, req *connect.Request[threadsv1.SendMessageRequest]) (*connect.Response[threadsv1.SendMessageResponse], error) {
	message := &threadsv1.Message{
		Id:       uuid.New().String(),
		ThreadId: req.Msg.GetThreadId(),
		SenderId: req.Msg.GetSenderId(),
		Body:     req.Msg.GetBody(),
		FileIds:  append([]string{}, req.Msg.GetFileIds()...),
	}
	m.mu.Lock()
	m.sentMessages = append(m.sentMessages, message)
	m.mu.Unlock()
	return connect.NewResponse(&threadsv1.SendMessageResponse{Message: message}), nil
}

func (m *mockGateway) GetUnackedMessages(_ context.Context, _ *connect.Request[threadsv1.GetUnackedMessagesRequest]) (*connect.Response[threadsv1.GetUnackedMessagesResponse], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	messages := make([]*threadsv1.Message, 0, len(m.unacked))
	for _, msg := range m.unacked {
		messages = append(messages, msg)
	}
	return connect.NewResponse(&threadsv1.GetUnackedMessagesResponse{Messages: messages}), nil
}

func (m *mockGateway) AckMessages(_ context.Context, req *connect.Request[threadsv1.AckMessagesRequest]) (*connect.Response[threadsv1.AckMessagesResponse], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range req.Msg.GetMessageIds() {
		delete(m.unacked, id)
	}
	return connect.NewResponse(&threadsv1.AckMessagesResponse{}), nil
}

func (m *mockGateway) UploadFile(_ context.Context, stream *connect.ClientStream[filesv1.UploadFileRequest]) (*connect.Response[filesv1.UploadFileResponse], error) {
	var metadata *filesv1.UploadFileMetadata
	var payload bytes.Buffer
	for stream.Receive() {
		msg := stream.Msg()
		switch data := msg.GetPayload().(type) {
		case *filesv1.UploadFileRequest_Metadata:
			metadata = data.Metadata
		case *filesv1.UploadFileRequest_Chunk:
			if _, err := payload.Write(data.Chunk.GetData()); err != nil {
				return nil, err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing metadata"))
	}
	id := uuid.New().String()
	info := &filesv1.FileInfo{
		Id:          id,
		Filename:    metadata.GetFilename(),
		ContentType: metadata.GetContentType(),
		SizeBytes:   int64(payload.Len()),
	}
	m.mu.Lock()
	m.files[id] = fileRecord{info: info, data: payload.Bytes()}
	m.mu.Unlock()
	return connect.NewResponse(&filesv1.UploadFileResponse{File: info}), nil
}

func (m *mockGateway) GetFileMetadata(_ context.Context, req *connect.Request[filesv1.GetFileMetadataRequest]) (*connect.Response[filesv1.GetFileMetadataResponse], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.files[req.Msg.GetFileId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("file not found"))
	}
	return connect.NewResponse(&filesv1.GetFileMetadataResponse{File: record.info}), nil
}

func (m *mockGateway) GetFileContent(_ context.Context, req *connect.Request[filesv1.GetFileContentRequest], stream *connect.ServerStream[filesv1.GetFileContentResponse]) error {
	m.mu.Lock()
	record, ok := m.files[req.Msg.GetFileId()]
	m.mu.Unlock()
	if !ok {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("file not found"))
	}
	if err := stream.Send(&filesv1.GetFileContentResponse{ChunkData: record.data}); err != nil {
		return err
	}
	return nil
}

func (m *mockGateway) Subscribe(ctx context.Context, _ *connect.Request[notificationsv1.SubscribeRequest], stream *connect.ServerStream[notificationsv1.SubscribeResponse]) error {
	return m.notifications.Subscribe(ctx, stream)
}

func (m *mockGateway) QueueUnackedMessage(message *threadsv1.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unacked[message.GetId()] = message
}

func (m *mockGateway) AddFile(filename, contentType string, data []byte) string {
	id := uuid.New().String()
	m.AddFileWithID(id, filename, contentType, data)
	return id
}

func (m *mockGateway) AddFileWithID(id, filename, contentType string, data []byte) {
	record := fileRecord{
		info: &filesv1.FileInfo{
			Id:          id,
			Filename:    filename,
			ContentType: contentType,
			SizeBytes:   int64(len(data)),
		},
		data: data,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[id] = record
}

func (m *mockGateway) SentMessages() []*threadsv1.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	messages := make([]*threadsv1.Message, len(m.sentMessages))
	copy(messages, m.sentMessages)
	return messages
}

func (m *mockGateway) UploadedFiles() map[string]fileRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	copyMap := make(map[string]fileRecord, len(m.files))
	for key, value := range m.files {
		copyMap[key] = value
	}
	return copyMap
}

func (m *mockGateway) UnackedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.unacked)
}

func (m *mockNotifications) Subscribe(ctx context.Context, stream *connect.ServerStream[notificationsv1.SubscribeResponse]) error {
	ch := make(chan *notificationsv1.SubscribeResponse, 1)
	m.mu.Lock()
	m.subscribers[ch] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.subscribers, ch)
		m.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-ch:
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

func (m *mockNotifications) Notify() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subscribers {
		select {
		case ch <- &notificationsv1.SubscribeResponse{Envelope: &notificationsv1.NotificationEnvelope{Id: uuid.New().String()}}:
		default:
		}
	}
}
