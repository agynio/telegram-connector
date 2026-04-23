package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
)

func TestSelectAllowedContentTypesPrefersTelegramMime(t *testing.T) {
	candidates := buildContentTypeCandidates("image/png", "", "", "application/octet-stream")
	allowed := selectAllowedContentTypes(candidates, false)
	if len(allowed) != 1 || allowed[0] != "image/png" {
		t.Fatalf("expected image/png, got %v", allowed)
	}
}

func TestSelectAllowedContentTypesUsesSniffedWhenTelegramMissing(t *testing.T) {
	candidates := buildContentTypeCandidates("", "image/jpeg", "", "application/octet-stream")
	allowed := selectAllowedContentTypes(candidates, false)
	if len(allowed) != 1 || allowed[0] != "image/jpeg" {
		t.Fatalf("expected image/jpeg, got %v", allowed)
	}
}

func TestSelectAllowedContentTypesAliases(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "zip", raw: "application/x-zip-compressed", want: "application/zip"},
		{name: "gzip", raw: "application/x-gzip", want: "application/gzip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := buildContentTypeCandidates(tt.raw, "", "", "")
			allowed := selectAllowedContentTypes(candidates, false)
			if len(allowed) != 1 || allowed[0] != tt.want {
				t.Fatalf("expected %s, got %v", tt.want, allowed)
			}
		})
	}
}

func TestUploadTelegramFileSkipsDisallowedContentTypes(t *testing.T) {
	token := "token"
	fileID := "file-123"
	filePath := "files/" + fileID + ".bin"
	data := bytes.Repeat([]byte{0x00}, 512)
	if detected := http.DetectContentType(data); detected != "application/octet-stream" {
		t.Fatalf("expected sniffed octet-stream, got %s", detected)
	}
	server := newTelegramFileServer(t, token, fileID, filePath, "application/octet-stream", data)
	defer server.Close()

	installationID := uuid.New()
	auditCalled := false
	gatewayStub := &stubGateway{
		t: t,
		appendAuditFn: func(_ context.Context, installationArg, message string, level appsv1.InstallationAuditLogLevel, idempotencyKey string) error {
			auditCalled = true
			if installationArg != installationID.String() {
				t.Fatalf("expected installation %s, got %s", installationID, installationArg)
			}
			if level != appsv1.InstallationAuditLogLevel_INSTALLATION_AUDIT_LOG_LEVEL_WARNING {
				t.Fatalf("expected warning level, got %v", level)
			}
			if !strings.Contains(message, auditEventAttachmentSkipped) {
				t.Fatalf("expected audit event %s, got %s", auditEventAttachmentSkipped, message)
			}
			if !strings.Contains(message, fileID) {
				t.Fatalf("expected file id in message, got %s", message)
			}
			if !strings.Contains(message, "application/octet-stream") {
				t.Fatalf("expected content types in message, got %s", message)
			}
			if strings.TrimSpace(idempotencyKey) == "" {
				t.Fatal("expected idempotency key")
			}
			return nil
		},
	}
	storeStub := &stubStore{t: t}
	worker := newInstallationWorker(Installation{
		ID:             installationID,
		OrganizationID: uuid.New(),
		BotToken:       token,
		AgentID:        uuid.New(),
	}, storeStub, gatewayStub, server.URL, time.Second)

	result, err := worker.uploadTelegramFile(context.Background(), fileID, "", "", false)
	if err != nil {
		t.Fatalf("uploadTelegramFile returned error: %v", err)
	}
	if result.fileID != "" {
		t.Fatalf("expected no upload id, got %s", result.fileID)
	}
	if !strings.Contains(result.skipMarker, "attachment skipped") {
		t.Fatalf("expected skip marker, got %s", result.skipMarker)
	}
	if !strings.Contains(result.skipMarker, "size=512") {
		t.Fatalf("expected size detail, got %s", result.skipMarker)
	}
	if !strings.Contains(result.skipMarker, "content_type=application/octet-stream") {
		t.Fatalf("expected content type detail, got %s", result.skipMarker)
	}
	if !auditCalled {
		t.Fatal("expected audit log call")
	}
}

func newTelegramFileServer(t *testing.T, token, fileID, filePath, contentType string, data []byte) *httptest.Server {
	t.Helper()
	getFilePath := "/bot" + token + "/getFile"
	filePrefix := "/file/bot" + token + "/"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == getFilePath:
			resp := map[string]any{
				"ok": true,
				"result": map[string]any{
					"file_id":   fileID,
					"file_path": filePath,
					"file_size": len(data),
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, filePrefix):
			if strings.TrimPrefix(r.URL.Path, filePrefix) != filePath {
				http.NotFound(w, r)
				return
			}
			if contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	})

	return httptest.NewServer(handler)
}
