//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/agynio/telegram-connector/internal/telegram"
)

type mockTelegram struct {
	token   string
	mu      sync.Mutex
	updates []telegram.Update
	files   map[string]telegramFile
	sends   []telegramSend
}

type telegramFile struct {
	fileID      string
	filePath    string
	data        []byte
	contentType string
}

type telegramSend struct {
	method      string
	chatID      int64
	text        string
	url         string
	filename    string
	contentType string
	data        []byte
}

func newMockTelegram(token string) *mockTelegram {
	return &mockTelegram{
		token: token,
		files: make(map[string]telegramFile),
	}
}

func (m *mockTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/file/bot"+m.token+"/") {
		m.handleFileDownload(w, r)
		return
	}
	prefix := "/bot" + m.token + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, prefix)
	switch method {
	case "getUpdates":
		m.handleGetUpdates(w, r)
	case "getFile":
		m.handleGetFile(w, r)
	case "sendMessage":
		m.handleSendMessage(w, r)
	case "sendPhoto":
		m.handleSendMedia(w, r, "sendPhoto", "photo")
	case "sendDocument":
		m.handleSendMedia(w, r, "sendDocument", "document")
	case "sendAudio":
		m.handleSendMedia(w, r, "sendAudio", "audio")
	case "sendVideo":
		m.handleSendMedia(w, r, "sendVideo", "video")
	case "sendVoice":
		m.handleSendMedia(w, r, "sendVoice", "voice")
	default:
		http.NotFound(w, r)
	}
}

func (m *mockTelegram) QueueUpdate(update telegram.Update) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates = append(m.updates, update)
}

func (m *mockTelegram) AddFile(fileID, filePath string, data []byte, contentType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[fileID] = telegramFile{fileID: fileID, filePath: filePath, data: data, contentType: contentType}
}

func (m *mockTelegram) Sends() []telegramSend {
	m.mu.Lock()
	defer m.mu.Unlock()
	sends := make([]telegramSend, len(m.sends))
	copy(sends, m.sends)
	return sends
}

func (m *mockTelegram) handleGetUpdates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Offset int64 `json:"offset"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	m.mu.Lock()
	updates := make([]telegram.Update, 0, len(m.updates))
	remaining := make([]telegram.Update, 0, len(m.updates))
	for _, update := range m.updates {
		if req.Offset == 0 || update.UpdateID >= req.Offset {
			updates = append(updates, update)
		} else {
			remaining = append(remaining, update)
		}
	}
	m.updates = remaining
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": updates})
}

func (m *mockTelegram) handleGetFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FileID string `json:"file_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "description": err.Error()})
		return
	}
	m.mu.Lock()
	file, ok := m.files[req.FileID]
	m.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "description": "file not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"file_id": file.fileID, "file_path": file.filePath, "file_size": len(file.data)}})
}

func (m *mockTelegram) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "description": err.Error()})
		return
	}
	m.recordSend(telegramSend{method: "sendMessage", chatID: req.ChatID, text: req.Text})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
}

func (m *mockTelegram) handleSendMedia(w http.ResponseWriter, r *http.Request, method, field string) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "description": err.Error()})
			return
		}
		chatID, _ := req["chat_id"].(float64)
		urlValue, _ := req[field].(string)
		m.recordSend(telegramSend{method: method, chatID: int64(chatID), url: urlValue})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
		return
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "description": err.Error()})
		return
	}
	chatIDValue := r.FormValue("chat_id")
	var chatID int64
	if chatIDValue != "" {
		chatID, _ = strconv.ParseInt(chatIDValue, 10, 64)
	}
	file, header, err := r.FormFile(field)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "description": err.Error()})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "description": err.Error()})
		return
	}
	contentType := header.Header.Get("Content-Type")
	m.recordSend(telegramSend{method: method, chatID: chatID, filename: header.Filename, contentType: contentType, data: data})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
}

func (m *mockTelegram) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	filePath := strings.TrimPrefix(r.URL.Path, "/file/bot"+m.token+"/")
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, file := range m.files {
		if file.filePath == filePath {
			if file.contentType != "" {
				w.Header().Set("Content-Type", file.contentType)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(file.data)
			return
		}
	}
	http.NotFound(w, r)
}

func (m *mockTelegram) recordSend(send telegramSend) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sends = append(m.sends, send)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
