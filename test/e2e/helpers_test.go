//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	notificationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/notifications/v1"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
	"github.com/agynio/telegram-connector/internal/telegram"
)

const (
	grpcTimeout     = 10 * time.Second
	pollInterval    = 1 * time.Second
	uploadChunkSize = 32 * 1024
)

var (
	chatSeq   atomic.Int64
	updateSeq atomic.Int64
)

type enqueueRequest struct {
	Token   string            `json:"token"`
	Updates []telegram.Update `json:"updates"`
}

type resetRequest struct {
	Token string `json:"token"`
}

type setFileRequest struct {
	Token       string `json:"token"`
	FileID      string `json:"file_id"`
	FilePath    string `json:"file_path"`
	ContentType string `json:"content_type"`
	DataBase64  string `json:"data_base64"`
}

type setSendErrorRequest struct {
	Token             string `json:"token"`
	StatusCode        int    `json:"status_code"`
	ErrorCode         int    `json:"error_code"`
	Description       string `json:"description"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
	Method            string `json:"method"`
}

type sentMessage struct {
	Method            string `json:"method"`
	ChatID            int64  `json:"chat_id"`
	Text              string `json:"text,omitempty"`
	URL               string `json:"url,omitempty"`
	Filename          string `json:"filename,omitempty"`
	ContentType       string `json:"content_type,omitempty"`
	DataBase64        string `json:"data_base64,omitempty"`
	StatusCode        int    `json:"status_code"`
	ErrorCode         int    `json:"error_code,omitempty"`
	Description       string `json:"description,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func waitForHealthy(url string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	client := httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	for time.Now().Before(deadline) {
		attemptTimeout := 5 * time.Second
		if remaining := time.Until(deadline); remaining < attemptTimeout {
			attemptTimeout = remaining
		}
		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					cancel()
					return
				}
			}
		}
		cancel()
		time.Sleep(time.Second)
	}
	fmt.Fprintf(os.Stderr, "e2e setup: %s not healthy within %s\n", url, timeout)
	os.Exit(1)
}

func resetTelegramMockToken(token string) error {
	return controlPost("/_control/reset", resetRequest{Token: token})
}

func resetTelegramMock(t *testing.T, token string) {
	t.Helper()
	require.NoError(t, resetTelegramMockToken(token))
}

func enqueueUpdate(t *testing.T, token string, update telegram.Update) {
	t.Helper()
	req := enqueueRequest{Token: token, Updates: []telegram.Update{update}}
	require.NoError(t, controlPost("/_control/enqueue", req))
}

func enqueueTextUpdate(t *testing.T, token string, chatID int64, text string) {
	t.Helper()
	update := telegram.Update{
		UpdateID: nextUpdateID(),
		Message: &telegram.Message{
			MessageID: nextUpdateID(),
			From:      &telegram.User{ID: chatID + 1000},
			Chat:      telegram.Chat{ID: chatID, Type: "private"},
			Text:      text,
		},
	}
	enqueueUpdate(t, token, update)
}

func enqueuePhotoUpdate(t *testing.T, token string, chatID int64, fileID string, caption string, size int64) {
	t.Helper()
	update := telegram.Update{
		UpdateID: nextUpdateID(),
		Message: &telegram.Message{
			MessageID: nextUpdateID(),
			From:      &telegram.User{ID: chatID + 2000},
			Chat:      telegram.Chat{ID: chatID, Type: "private"},
			Caption:   caption,
			Photo:     []telegram.Photo{{FileID: fileID, FileSize: size, Width: 800, Height: 600}},
		},
	}
	enqueueUpdate(t, token, update)
}

func setTelegramFile(t *testing.T, token, fileID, filePath, contentType string, data []byte) {
	t.Helper()
	req := setFileRequest{
		Token:       token,
		FileID:      fileID,
		FilePath:    filePath,
		ContentType: contentType,
		DataBase64:  base64.StdEncoding.EncodeToString(data),
	}
	require.NoError(t, controlPost("/_control/set-file", req))
}

func setSendError(t *testing.T, token string, statusCode int, errorCode int, description string, retryAfter time.Duration, method string) {
	t.Helper()
	req := setSendErrorRequest{
		Token:             token,
		StatusCode:        statusCode,
		ErrorCode:         errorCode,
		Description:       description,
		RetryAfterSeconds: int(retryAfter.Seconds()),
		Method:            method,
	}
	require.NoError(t, controlPost("/_control/set-send-error", req))
}

func getSentMessages(t *testing.T, token string) []sentMessage {
	t.Helper()
	url := fmt.Sprintf("/_control/sent-messages?token=%s", token)
	var resp struct {
		Messages []sentMessage `json:"messages"`
	}
	require.NoError(t, controlGet(url, &resp))
	return resp.Messages
}

func waitForSentMessage(t *testing.T, token string, predicate func(sentMessage) bool, timeout time.Duration) []sentMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		messages := getSentMessages(t, token)
		for _, message := range messages {
			if predicate(message) {
				return messages
			}
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("timed out waiting for telegram send")
	return nil
}

func decodeSendData(t *testing.T, message sentMessage) []byte {
	t.Helper()
	if message.DataBase64 == "" {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(message.DataBase64)
	require.NoError(t, err)
	return data
}

func waitForMessage(t *testing.T, body string, fileCount int, timeout time.Duration) (string, *threadsv1.Message) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		threadID, message, found, err := findMessage(body, fileCount)
		require.NoError(t, err)
		if found {
			return threadID, message
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("timed out waiting for message %q", body)
	return "", nil
}

func findMessage(body string, fileCount int) (string, *threadsv1.Message, bool, error) {
	threads, err := listThreads()
	if err != nil {
		return "", nil, false, err
	}
	for _, thread := range threads {
		messages, err := listMessages(thread.GetId())
		if err != nil {
			return "", nil, false, err
		}
		for _, message := range messages {
			if message.GetBody() != body {
				continue
			}
			if fileCount >= 0 && len(message.GetFileIds()) != fileCount {
				continue
			}
			return thread.GetId(), message, true, nil
		}
	}
	return "", nil, false, nil
}

func messageExists(t *testing.T, body string) bool {
	t.Helper()
	_, _, found, err := findMessage(body, -1)
	require.NoError(t, err)
	return found
}

func assertNoMessage(t *testing.T, body string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if messageExists(t, body) {
			t.Fatalf("unexpected message %q", body)
		}
		time.Sleep(pollInterval)
	}
}

func ensureChatMapping(t *testing.T, token string, chatID int64) string {
	t.Helper()
	body := fmt.Sprintf("mapping %d", time.Now().UnixNano())
	enqueueTextUpdate(t, token, chatID, body)
	threadID, _ := waitForMessage(t, body, 0, 90*time.Second)
	return threadID
}

func listThreads() ([]*threadsv1.Thread, error) {
	ctx, cancel := context.WithTimeout(context.Background(), grpcTimeout)
	defer cancel()
	threads := make([]*threadsv1.Thread, 0)
	pageToken := ""
	for {
		resp, err := threadsClient.GetThreads(ctx, &threadsv1.GetThreadsRequest{
			ParticipantId: agentIdentityID,
			PageSize:      100,
			PageToken:     pageToken,
		})
		if err != nil {
			return nil, err
		}
		threads = append(threads, resp.GetThreads()...)
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			return threads, nil
		}
	}
}

func listMessages(threadID string) ([]*threadsv1.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), grpcTimeout)
	defer cancel()
	messages := make([]*threadsv1.Message, 0)
	pageToken := ""
	for {
		resp, err := threadsClient.GetMessages(ctx, &threadsv1.GetMessagesRequest{
			ThreadId:  threadID,
			PageSize:  100,
			PageToken: pageToken,
		})
		if err != nil {
			return nil, err
		}
		messages = append(messages, resp.GetMessages()...)
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			return messages, nil
		}
	}
}

func sendThreadMessage(t *testing.T, threadID, body string, fileIDs []string) *threadsv1.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), grpcTimeout)
	defer cancel()
	resp, err := threadsClient.SendMessage(ctx, &threadsv1.SendMessageRequest{
		ThreadId: threadID,
		SenderId: agentIdentityID,
		Body:     body,
		FileIds:  fileIDs,
	})
	require.NoError(t, err)
	return resp.GetMessage()
}

func waitForMessageAck(t *testing.T, body string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		messages, err := listUnackedMessages()
		require.NoError(t, err)
		if !containsMessageBody(messages, body) {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("timed out waiting for ack on message %q", body)
}

func listUnackedMessages() ([]*threadsv1.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), grpcTimeout)
	defer cancel()
	messages := make([]*threadsv1.Message, 0)
	pageToken := ""
	for {
		resp, err := threadsClient.GetUnackedMessages(ctx, &threadsv1.GetUnackedMessagesRequest{
			ParticipantId: appIdentityID,
			PageSize:      100,
			PageToken:     pageToken,
		})
		if err != nil {
			return nil, err
		}
		messages = append(messages, resp.GetMessages()...)
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			return messages, nil
		}
	}
}

func containsMessageBody(messages []*threadsv1.Message, body string) bool {
	for _, message := range messages {
		if message.GetBody() == body {
			return true
		}
	}
	return false
}

func notifyOutbound(t *testing.T) {
	t.Helper()
	payload, err := structpb.NewStruct(map[string]any{"source": "e2e"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), grpcTimeout)
	defer cancel()
	_, err = notificationsClient.Publish(ctx, &notificationsv1.PublishRequest{
		Event:   "e2e",
		Rooms:   []string{"e2e"},
		Payload: payload,
		Source:  "e2e",
	})
	require.NoError(t, err)
}

func uploadFile(t *testing.T, filename, contentType string, data []byte) *filesv1.FileInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), grpcTimeout)
	defer cancel()
	stream, err := filesClient.UploadFile(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&filesv1.UploadFileRequest{
		Payload: &filesv1.UploadFileRequest_Metadata{Metadata: &filesv1.UploadFileMetadata{
			Filename:    filename,
			ContentType: contentType,
			SizeBytes:   int64(len(data)),
		}},
	}))
	reader := bytes.NewReader(data)
	buf := make([]byte, uploadChunkSize)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			require.NoError(t, stream.Send(&filesv1.UploadFileRequest{
				Payload: &filesv1.UploadFileRequest_Chunk{Chunk: &filesv1.UploadFileChunk{Data: buf[:n]}},
			}))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err)
		}
	}
	resp, err := stream.CloseAndRecv()
	require.NoError(t, err)
	file := resp.GetFile()
	require.NotNil(t, file)
	require.NotEmpty(t, file.GetId())
	return file
}

func getFileMetadata(t *testing.T, fileID string) *filesv1.FileInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), grpcTimeout)
	defer cancel()
	resp, err := filesClient.GetFileMetadata(ctx, &filesv1.GetFileMetadataRequest{FileId: fileID})
	require.NoError(t, err)
	return resp.GetFile()
}

func downloadFile(t *testing.T, fileID string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), grpcTimeout)
	defer cancel()
	stream, err := filesClient.GetFileContent(ctx, &filesv1.GetFileContentRequest{FileId: fileID})
	require.NoError(t, err)
	var buffer bytes.Buffer
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		buffer.Write(resp.GetChunkData())
	}
	return buffer.Bytes()
}

func controlPost(path string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://%s%s", telegramMockAddress, path)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("control %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	return nil
}

func controlGet(path string, target any) error {
	url := fmt.Sprintf("http://%s%s", telegramMockAddress, path)
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("control %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func nextChatID() int64 {
	return 900000 + chatSeq.Add(1)
}

func nextUpdateID() int64 {
	return updateSeq.Add(1)
}
