package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/openziti/sdk-golang/ziti"
	"google.golang.org/protobuf/proto"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	"github.com/agynio/telegram-connector/.gen/go/agynio/api/gateway/v1/gatewayv1connect"
	notificationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/notifications/v1"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
)

const uploadChunkSize = 32 * 1024

type Client struct {
	appIdentityID string
	apps          gatewayv1connect.AppsGatewayClient
	threads       gatewayv1connect.ThreadsGatewayClient
	files         gatewayv1connect.FilesGatewayClient
	notifications gatewayv1connect.NotificationsGatewayClient
}

func NewZitiHTTPClient(zitiCtx ziti.Context) *http.Client {
	transport := &http.Transport{
		DialContext: func(_ context.Context, _, addr string) (net.Conn, error) {
			svc := addr
			if host, _, err := net.SplitHostPort(addr); err == nil {
				svc = host
			}
			return zitiCtx.Dial(svc)
		},
	}
	return &http.Client{Transport: transport}
}

func NewClient(httpClient connect.HTTPClient, baseURL, appIdentityID string) *Client {
	return &Client{
		appIdentityID: appIdentityID,
		apps:          gatewayv1connect.NewAppsGatewayClient(httpClient, baseURL),
		threads:       gatewayv1connect.NewThreadsGatewayClient(httpClient, baseURL),
		files:         gatewayv1connect.NewFilesGatewayClient(httpClient, baseURL),
		notifications: gatewayv1connect.NewNotificationsGatewayClient(httpClient, baseURL),
	}
}

func (c *Client) AppIdentityID() string {
	return c.appIdentityID
}

func (c *Client) ListInstallations(ctx context.Context, appID string) ([]*appsv1.Installation, error) {
	var installations []*appsv1.Installation
	pageToken := ""
	for {
		req := &appsv1.ListInstallationsRequest{
			PageSize:  100,
			PageToken: pageToken,
		}
		if appID != "" {
			req.AppId = appID
		}
		resp, err := c.apps.ListInstallations(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, fmt.Errorf("list installations: %w", err)
		}
		installations = append(installations, resp.Msg.GetInstallations()...)
		pageToken = resp.Msg.GetNextPageToken()
		if pageToken == "" {
			return installations, nil
		}
	}
}

func (c *Client) ReportInstallationStatus(ctx context.Context, installationID, status string) error {
	_, err := c.apps.ReportInstallationStatus(ctx, connect.NewRequest(&appsv1.ReportInstallationStatusRequest{
		InstallationId: installationID,
		Status:         status,
	}))
	if err != nil {
		return fmt.Errorf("report installation status: %w", err)
	}
	return nil
}

func (c *Client) AppendInstallationAuditLogEntry(ctx context.Context, installationID, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey string) error {
	key := strings.TrimSpace(idempotencyKey)
	if key == "" {
		return fmt.Errorf("append installation audit log entry: idempotency key required")
	}
	request := &appsv1.AppendInstallationAuditLogEntryRequest{
		InstallationId: installationID,
		Message:        message,
		Level:          level,
		IdempotencyKey: &key,
	}
	_, err := c.apps.AppendInstallationAuditLogEntry(ctx, connect.NewRequest(request))
	if err != nil {
		return fmt.Errorf("append installation audit log entry: %w", err)
	}
	return nil
}

func (c *Client) CreateThread(ctx context.Context, organizationID string) (*threadsv1.Thread, error) {
	resp, err := c.threads.CreateThread(ctx, connect.NewRequest(&threadsv1.CreateThreadRequest{
		OrganizationId: proto.String(organizationID),
	}))
	if err != nil {
		return nil, fmt.Errorf("create thread: %w", err)
	}
	thread := resp.Msg.GetThread()
	if thread == nil {
		return nil, fmt.Errorf("create thread: empty thread")
	}
	return thread, nil
}

func (c *Client) AddParticipant(ctx context.Context, organizationID, threadID, participantID string) error {
	_, err := c.threads.AddParticipant(ctx, connect.NewRequest(&threadsv1.AddParticipantRequest{
		ThreadId:       threadID,
		OrganizationId: proto.String(organizationID),
		Participant:    &threadsv1.ParticipantIdentifier{Identifier: &threadsv1.ParticipantIdentifier_ParticipantId{ParticipantId: participantID}},
		Passive:        false,
	}))
	if err != nil {
		return fmt.Errorf("add participant: %w", err)
	}
	return nil
}

func (c *Client) SendMessage(ctx context.Context, threadID, body string, fileIDs []string) error {
	if body == "" && len(fileIDs) == 0 {
		return fmt.Errorf("send message: body or files required")
	}
	_, err := c.threads.SendMessage(ctx, connect.NewRequest(&threadsv1.SendMessageRequest{
		ThreadId: threadID,
		SenderId: c.appIdentityID,
		Body:     body,
		FileIds:  fileIDs,
	}))
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return nil
}

func (c *Client) GetUnackedMessages(ctx context.Context) ([]*threadsv1.Message, error) {
	var messages []*threadsv1.Message
	pageToken := ""
	for {
		resp, err := c.threads.GetUnackedMessages(ctx, connect.NewRequest(&threadsv1.GetUnackedMessagesRequest{
			ParticipantId: c.appIdentityID,
			PageSize:      100,
			PageToken:     pageToken,
		}))
		if err != nil {
			return nil, fmt.Errorf("get unacked messages: %w", err)
		}
		messages = append(messages, resp.Msg.GetMessages()...)
		pageToken = resp.Msg.GetNextPageToken()
		if pageToken == "" {
			return messages, nil
		}
	}
}

func (c *Client) AckMessages(ctx context.Context, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	_, err := c.threads.AckMessages(ctx, connect.NewRequest(&threadsv1.AckMessagesRequest{
		ParticipantId: c.appIdentityID,
		MessageIds:    messageIDs,
	}))
	if err != nil {
		return fmt.Errorf("ack messages: %w", err)
	}
	return nil
}

func (c *Client) UploadFile(ctx context.Context, metadata *filesv1.UploadFileMetadata, payload []byte) (*filesv1.FileInfo, error) {
	stream := c.files.UploadFile(ctx)
	if err := stream.Send(&filesv1.UploadFileRequest{
		Payload: &filesv1.UploadFileRequest_Metadata{Metadata: metadata},
	}); err != nil {
		return nil, fmt.Errorf("upload file metadata: %w", err)
	}

	reader := bytes.NewReader(payload)
	buf := make([]byte, uploadChunkSize)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if err := stream.Send(&filesv1.UploadFileRequest{
				Payload: &filesv1.UploadFileRequest_Chunk{Chunk: &filesv1.UploadFileChunk{Data: buf[:n]}},
			}); err != nil {
				return nil, fmt.Errorf("upload file chunk: %w", err)
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("read upload payload: %w", err)
		}
	}

	resp, err := stream.CloseAndReceive()
	if err != nil {
		return nil, fmt.Errorf("finalize upload: %w", err)
	}
	file := resp.Msg.GetFile()
	if file == nil || file.GetId() == "" {
		return nil, fmt.Errorf("upload file: empty file response")
	}
	return file, nil
}

func (c *Client) GetFileMetadata(ctx context.Context, fileID string) (*filesv1.FileInfo, error) {
	resp, err := c.files.GetFileMetadata(ctx, connect.NewRequest(&filesv1.GetFileMetadataRequest{FileId: fileID}))
	if err != nil {
		return nil, fmt.Errorf("get file metadata: %w", err)
	}
	file := resp.Msg.GetFile()
	if file == nil {
		return nil, fmt.Errorf("get file metadata: empty file")
	}
	return file, nil
}

func (c *Client) GetFileContent(ctx context.Context, fileID string) ([]byte, error) {
	stream, err := c.files.GetFileContent(ctx, connect.NewRequest(&filesv1.GetFileContentRequest{FileId: fileID}))
	if err != nil {
		return nil, fmt.Errorf("get file content: %w", err)
	}
	var payload bytes.Buffer
	for stream.Receive() {
		chunk := stream.Msg().GetChunkData()
		if _, err := payload.Write(chunk); err != nil {
			_ = stream.Close()
			return nil, fmt.Errorf("collect file content: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("stream file content: %w", err)
	}
	return payload.Bytes(), nil
}

func (c *Client) Subscribe(ctx context.Context) (*connect.ServerStreamForClient[notificationsv1.SubscribeResponse], error) {
	appIdentityID := strings.TrimSpace(c.appIdentityID)
	if appIdentityID == "" {
		return nil, fmt.Errorf("subscribe notifications: app identity id required")
	}
	request := &notificationsv1.SubscribeRequest{
		Rooms: []string{fmt.Sprintf("thread_participant:%s", appIdentityID)},
	}
	return c.notifications.Subscribe(ctx, connect.NewRequest(request))
}
