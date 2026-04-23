//go:build e2e

package e2e

import (
	"context"
	"errors"
	"io"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	"github.com/agynio/telegram-connector/.gen/go/agynio/api/gateway/v1/gatewayv1connect"
	notificationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/notifications/v1"
	organizationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/organizations/v1"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
)

type appsService struct {
	appsv1.UnimplementedAppsServiceServer
	state *testState
}

func (s *appsService) CreateApp(_ context.Context, req *appsv1.CreateAppRequest) (*appsv1.CreateAppResponse, error) {
	app, token, err := s.state.createApp(req)
	if err != nil {
		return nil, err
	}
	return &appsv1.CreateAppResponse{App: app, ServiceToken: token}, nil
}

func (s *appsService) InstallApp(_ context.Context, req *appsv1.InstallAppRequest) (*appsv1.InstallAppResponse, error) {
	installation, err := s.state.installApp(req.GetAppId(), req.GetOrganizationId(), req.GetConfiguration())
	if err != nil {
		return nil, err
	}
	return &appsv1.InstallAppResponse{Installation: installation}, nil
}

func (s *appsService) UpdateInstallation(_ context.Context, req *appsv1.UpdateInstallationRequest) (*appsv1.UpdateInstallationResponse, error) {
	installation, err := s.state.updateInstallation(req.GetId(), req.GetConfiguration())
	if err != nil {
		return nil, err
	}
	return &appsv1.UpdateInstallationResponse{Installation: installation}, nil
}

func (s *appsService) UninstallApp(_ context.Context, req *appsv1.UninstallAppRequest) (*appsv1.UninstallAppResponse, error) {
	if err := s.state.uninstallApp(req.GetId()); err != nil {
		return nil, err
	}
	return &appsv1.UninstallAppResponse{}, nil
}

func (s *appsService) DeleteApp(_ context.Context, req *appsv1.DeleteAppRequest) (*appsv1.DeleteAppResponse, error) {
	s.state.deleteApp(req.GetId())
	return &appsv1.DeleteAppResponse{}, nil
}

type organizationsService struct {
	organizationsv1.UnimplementedOrganizationsServiceServer
	state *testState
}

func (s *organizationsService) CreateOrganization(_ context.Context, req *organizationsv1.CreateOrganizationRequest) (*organizationsv1.CreateOrganizationResponse, error) {
	organization := s.state.createOrganization(req.GetName())
	return &organizationsv1.CreateOrganizationResponse{Organization: organization}, nil
}

func (s *organizationsService) DeleteOrganization(_ context.Context, req *organizationsv1.DeleteOrganizationRequest) (*organizationsv1.DeleteOrganizationResponse, error) {
	s.state.deleteOrganization(req.GetId())
	return &organizationsv1.DeleteOrganizationResponse{}, nil
}

type threadsService struct {
	threadsv1.UnimplementedThreadsServiceServer
	state *testState
}

func (s *threadsService) CreateThread(_ context.Context, req *threadsv1.CreateThreadRequest) (*threadsv1.CreateThreadResponse, error) {
	thread := s.state.createThread(req.GetOrganizationId())
	return &threadsv1.CreateThreadResponse{Thread: thread}, nil
}

func (s *threadsService) AddParticipant(_ context.Context, req *threadsv1.AddParticipantRequest) (*threadsv1.AddParticipantResponse, error) {
	participantID := ""
	if req.GetParticipant() != nil {
		participantID = req.GetParticipant().GetParticipantId()
	}
	if participantID == "" {
		return nil, status.Error(codes.InvalidArgument, "participant_id required")
	}
	thread, err := s.state.addParticipant(req.GetThreadId(), participantID, req.GetPassive())
	if err != nil {
		return nil, err
	}
	return &threadsv1.AddParticipantResponse{Thread: thread}, nil
}

func (s *threadsService) SendMessage(_ context.Context, req *threadsv1.SendMessageRequest) (*threadsv1.SendMessageResponse, error) {
	message, err := s.state.sendMessage(req.GetThreadId(), req.GetSenderId(), req.GetBody(), req.GetFileIds())
	if err != nil {
		return nil, err
	}
	return &threadsv1.SendMessageResponse{Message: message}, nil
}

func (s *threadsService) GetThreads(_ context.Context, req *threadsv1.GetThreadsRequest) (*threadsv1.GetThreadsResponse, error) {
	threads := s.state.listThreads(req.GetParticipantId())
	return &threadsv1.GetThreadsResponse{Threads: threads}, nil
}

func (s *threadsService) GetMessages(_ context.Context, req *threadsv1.GetMessagesRequest) (*threadsv1.GetMessagesResponse, error) {
	messages, err := s.state.listMessages(req.GetThreadId())
	if err != nil {
		return nil, err
	}
	return &threadsv1.GetMessagesResponse{Messages: messages}, nil
}

func (s *threadsService) GetUnackedMessages(_ context.Context, req *threadsv1.GetUnackedMessagesRequest) (*threadsv1.GetUnackedMessagesResponse, error) {
	messages := s.state.listUnackedMessages(req.GetParticipantId())
	return &threadsv1.GetUnackedMessagesResponse{Messages: messages}, nil
}

func (s *threadsService) AckMessages(_ context.Context, req *threadsv1.AckMessagesRequest) (*threadsv1.AckMessagesResponse, error) {
	s.state.ackMessages(req.GetParticipantId(), req.GetMessageIds())
	return &threadsv1.AckMessagesResponse{}, nil
}

type filesService struct {
	filesv1.UnimplementedFilesServiceServer
	state *testState
}

func (s *filesService) UploadFile(stream filesv1.FilesService_UploadFileServer) error {
	var metadata *filesv1.UploadFileMetadata
	data := make([]byte, 0)
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if meta := req.GetMetadata(); meta != nil {
			metadata = meta
			continue
		}
		if chunk := req.GetChunk(); chunk != nil {
			data = append(data, chunk.GetData()...)
		}
	}
	if metadata == nil {
		return status.Error(codes.InvalidArgument, "metadata required")
	}
	file := s.state.addFile(metadata, data)
	return stream.SendAndClose(&filesv1.UploadFileResponse{File: file})
}

func (s *filesService) GetFileMetadata(_ context.Context, req *filesv1.GetFileMetadataRequest) (*filesv1.GetFileMetadataResponse, error) {
	info, _, err := s.state.getFile(req.GetFileId())
	if err != nil {
		return nil, err
	}
	return &filesv1.GetFileMetadataResponse{File: info}, nil
}

func (s *filesService) GetFileContent(req *filesv1.GetFileContentRequest, stream filesv1.FilesService_GetFileContentServer) error {
	_, data, err := s.state.getFile(req.GetFileId())
	if err != nil {
		return err
	}
	return stream.Send(&filesv1.GetFileContentResponse{ChunkData: data})
}

type notificationsService struct {
	notificationsv1.UnimplementedNotificationsServiceServer
	state *testState
}

func (s *notificationsService) Publish(_ context.Context, req *notificationsv1.PublishRequest) (*notificationsv1.PublishResponse, error) {
	envelope := &notificationsv1.NotificationEnvelope{
		Id:      uuid.NewString(),
		Ts:      timestamppb.Now(),
		Source:  req.GetSource(),
		Event:   req.GetEvent(),
		Rooms:   req.GetRooms(),
		Payload: req.GetPayload(),
	}
	s.state.publishNotification(envelope)
	return &notificationsv1.PublishResponse{Id: envelope.Id, Ts: envelope.Ts}, nil
}

type appsGatewayServer struct {
	gatewayv1connect.UnimplementedAppsGatewayHandler
	state *testState
}

func (s *appsGatewayServer) ListInstallations(_ context.Context, req *connect.Request[appsv1.ListInstallationsRequest]) (*connect.Response[appsv1.ListInstallationsResponse], error) {
	installations := s.state.listInstallations(req.Msg.GetAppId())
	return connect.NewResponse(&appsv1.ListInstallationsResponse{Installations: installations}), nil
}

func (s *appsGatewayServer) ReportInstallationStatus(_ context.Context, req *connect.Request[appsv1.ReportInstallationStatusRequest]) (*connect.Response[appsv1.ReportInstallationStatusResponse], error) {
	if err := s.state.reportInstallationStatus(req.Msg.GetInstallationId(), req.Msg.GetStatus()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&appsv1.ReportInstallationStatusResponse{}), nil
}

func (s *appsGatewayServer) AppendInstallationAuditLogEntry(_ context.Context, req *connect.Request[appsv1.AppendInstallationAuditLogEntryRequest]) (*connect.Response[appsv1.AppendInstallationAuditLogEntryResponse], error) {
	entry := s.state.appendAudit(req.Msg.GetInstallationId(), req.Msg.GetMessage(), req.Msg.GetLevel(), req.Msg.IdempotencyKey)
	return connect.NewResponse(&appsv1.AppendInstallationAuditLogEntryResponse{Entry: entry}), nil
}

type threadsGatewayServer struct {
	gatewayv1connect.UnimplementedThreadsGatewayHandler
	state *testState
}

func (s *threadsGatewayServer) CreateThread(_ context.Context, req *connect.Request[threadsv1.CreateThreadRequest]) (*connect.Response[threadsv1.CreateThreadResponse], error) {
	thread := s.state.createThread(req.Msg.GetOrganizationId())
	return connect.NewResponse(&threadsv1.CreateThreadResponse{Thread: thread}), nil
}

func (s *threadsGatewayServer) AddParticipant(_ context.Context, req *connect.Request[threadsv1.AddParticipantRequest]) (*connect.Response[threadsv1.AddParticipantResponse], error) {
	participantID := ""
	if req.Msg.GetParticipant() != nil {
		participantID = req.Msg.GetParticipant().GetParticipantId()
	}
	if participantID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, status.Error(codes.InvalidArgument, "participant_id required"))
	}
	thread, err := s.state.addParticipant(req.Msg.GetThreadId(), participantID, req.Msg.GetPassive())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&threadsv1.AddParticipantResponse{Thread: thread}), nil
}

func (s *threadsGatewayServer) SendMessage(_ context.Context, req *connect.Request[threadsv1.SendMessageRequest]) (*connect.Response[threadsv1.SendMessageResponse], error) {
	message, err := s.state.sendMessage(req.Msg.GetThreadId(), req.Msg.GetSenderId(), req.Msg.GetBody(), req.Msg.GetFileIds())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&threadsv1.SendMessageResponse{Message: message}), nil
}

func (s *threadsGatewayServer) GetUnackedMessages(_ context.Context, req *connect.Request[threadsv1.GetUnackedMessagesRequest]) (*connect.Response[threadsv1.GetUnackedMessagesResponse], error) {
	messages := s.state.listUnackedMessages(req.Msg.GetParticipantId())
	return connect.NewResponse(&threadsv1.GetUnackedMessagesResponse{Messages: messages}), nil
}

func (s *threadsGatewayServer) AckMessages(_ context.Context, req *connect.Request[threadsv1.AckMessagesRequest]) (*connect.Response[threadsv1.AckMessagesResponse], error) {
	s.state.ackMessages(req.Msg.GetParticipantId(), req.Msg.GetMessageIds())
	return connect.NewResponse(&threadsv1.AckMessagesResponse{}), nil
}

type filesGatewayServer struct {
	gatewayv1connect.UnimplementedFilesGatewayHandler
	state *testState
}

func (s *filesGatewayServer) UploadFile(_ context.Context, stream *connect.ClientStream[filesv1.UploadFileRequest]) (*connect.Response[filesv1.UploadFileResponse], error) {
	var metadata *filesv1.UploadFileMetadata
	data := make([]byte, 0)
	for stream.Receive() {
		req := stream.Msg()
		if meta := req.GetMetadata(); meta != nil {
			metadata = meta
			continue
		}
		if chunk := req.GetChunk(); chunk != nil {
			data = append(data, chunk.GetData()...)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, status.Error(codes.InvalidArgument, "metadata required"))
	}
	file := s.state.addFile(metadata, data)
	return connect.NewResponse(&filesv1.UploadFileResponse{File: file}), nil
}

func (s *filesGatewayServer) GetFileMetadata(_ context.Context, req *connect.Request[filesv1.GetFileMetadataRequest]) (*connect.Response[filesv1.GetFileMetadataResponse], error) {
	info, _, err := s.state.getFile(req.Msg.GetFileId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&filesv1.GetFileMetadataResponse{File: info}), nil
}

func (s *filesGatewayServer) GetFileContent(_ context.Context, req *connect.Request[filesv1.GetFileContentRequest], stream *connect.ServerStream[filesv1.GetFileContentResponse]) error {
	_, data, err := s.state.getFile(req.Msg.GetFileId())
	if err != nil {
		return err
	}
	return stream.Send(&filesv1.GetFileContentResponse{ChunkData: data})
}

type notificationsGatewayServer struct {
	gatewayv1connect.UnimplementedNotificationsGatewayHandler
	state *testState
}

func (s *notificationsGatewayServer) Subscribe(ctx context.Context, _ *connect.Request[notificationsv1.SubscribeRequest], stream *connect.ServerStream[notificationsv1.SubscribeResponse]) error {
	id, ch := s.state.subscribeNotifications()
	defer s.state.unsubscribeNotifications(id)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(message); err != nil {
				return err
			}
		}
	}
}
