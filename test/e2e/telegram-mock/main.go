package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agynio/telegram-connector/internal/telegram"
)

const defaultAddress = ":8443"

type server struct {
	mu     sync.Mutex
	tokens map[string]*tokenState
}

type tokenState struct {
	updates    []telegram.Update
	files      map[string]telegramFile
	sends      []telegramSend
	sendErrors []sendError
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
	statusCode  int
	errorCode   int
	description string
	retryAfter  time.Duration
}

type sendError struct {
	statusCode  int
	errorCode   int
	description string
	retryAfter  time.Duration
	method      string
}

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

func main() {
	address := os.Getenv("TELEGRAM_MOCK_ADDRESS")
	if address == "" {
		address = defaultAddress
	}

	srv := newServer()
	log.Printf("telegram-mock listening on %s", address)
	if err := http.ListenAndServe(address, srv); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("telegram-mock failed: %v", err)
	}
}

func newServer() *server {
	return &server{tokens: make(map[string]*tokenState)}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/_control/"):
		s.handleControl(w, r)
	case strings.HasPrefix(r.URL.Path, "/file/"):
		s.handleFileDownload(w, r)
	case strings.HasPrefix(r.URL.Path, "/bot"):
		s.handleBotAPI(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *server) handleControl(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/_control/healthz":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "/_control/enqueue":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r)
			return
		}
		var req enqueueRequest
		if err := decodeJSON(r.Body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse(err))
			return
		}
		if req.Token == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse(fmt.Errorf("token is required")))
			return
		}
		if len(req.Updates) == 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse(fmt.Errorf("updates are required")))
			return
		}
		s.enqueueUpdates(req.Token, req.Updates)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "/_control/reset":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r)
			return
		}
		var req resetRequest
		if err := decodeJSON(r.Body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse(err))
			return
		}
		s.resetState(req.Token)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "/_control/set-file":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r)
			return
		}
		var req setFileRequest
		if err := decodeJSON(r.Body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse(err))
			return
		}
		if req.Token == "" || req.FileID == "" || req.FilePath == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse(fmt.Errorf("token, file_id, and file_path are required")))
			return
		}
		data, err := base64.StdEncoding.DecodeString(req.DataBase64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse(fmt.Errorf("invalid data_base64: %w", err)))
			return
		}
		s.setFile(req.Token, telegramFile{
			fileID:      req.FileID,
			filePath:    req.FilePath,
			data:        data,
			contentType: req.ContentType,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "/_control/set-send-error":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, r)
			return
		}
		var req setSendErrorRequest
		if err := decodeJSON(r.Body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse(err))
			return
		}
		if req.Token == "" || req.StatusCode == 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse(fmt.Errorf("token and status_code are required")))
			return
		}
		s.enqueueSendError(req.Token, sendError{
			statusCode:  req.StatusCode,
			errorCode:   req.ErrorCode,
			description: req.Description,
			retryAfter:  time.Duration(req.RetryAfterSeconds) * time.Second,
			method:      req.Method,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "/_control/sent-messages":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r)
			return
		}
		token := r.URL.Query().Get("token")
		if token == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse(fmt.Errorf("token is required")))
			return
		}
		messages := s.sentMessages(token)
		writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
	default:
		http.NotFound(w, r)
	}
}

func (s *server) handleBotAPI(w http.ResponseWriter, r *http.Request) {
	token, method, ok := parseBotPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch method {
	case "getUpdates":
		s.handleGetUpdates(w, r, token)
	case "getFile":
		s.handleGetFile(w, r, token)
	case "sendMessage":
		s.handleSendMessage(w, r, token)
	case "sendPhoto":
		s.handleSendMedia(w, r, token, "sendPhoto", "photo")
	case "sendDocument":
		s.handleSendMedia(w, r, token, "sendDocument", "document")
	case "sendAudio":
		s.handleSendMedia(w, r, token, "sendAudio", "audio")
	case "sendVideo":
		s.handleSendMedia(w, r, token, "sendVideo", "video")
	case "sendVoice":
		s.handleSendMedia(w, r, token, "sendVoice", "voice")
	default:
		http.NotFound(w, r)
	}
}

func (s *server) handleGetUpdates(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r)
		return
	}
	var req struct {
		Offset int64 `json:"offset"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		writeTelegramError(w, http.StatusBadRequest, http.StatusBadRequest, err.Error(), 0)
		return
	}
	updates := s.consumeUpdates(token, req.Offset)
	writeTelegramOK(w, updates)
}

func (s *server) handleGetFile(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r)
		return
	}
	var req struct {
		FileID string `json:"file_id"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		writeTelegramError(w, http.StatusBadRequest, http.StatusBadRequest, err.Error(), 0)
		return
	}
	if req.FileID == "" {
		writeTelegramError(w, http.StatusBadRequest, http.StatusBadRequest, "file_id is required", 0)
		return
	}
	file, ok := s.fileByID(token, req.FileID)
	if !ok {
		writeTelegramError(w, http.StatusNotFound, http.StatusNotFound, "file not found", 0)
		return
	}
	writeTelegramOK(w, map[string]any{
		"file_id":   file.fileID,
		"file_path": file.filePath,
		"file_size": len(file.data),
	})
}

func (s *server) handleSendMessage(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r)
		return
	}
	var req struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		writeTelegramError(w, http.StatusBadRequest, http.StatusBadRequest, err.Error(), 0)
		return
	}
	if req.ChatID == 0 {
		writeTelegramError(w, http.StatusBadRequest, http.StatusBadRequest, "chat_id is required", 0)
		return
	}
	send := telegramSend{method: "sendMessage", chatID: req.ChatID, text: req.Text}
	s.respondSend(w, token, send)
}

func (s *server) handleSendMedia(w http.ResponseWriter, r *http.Request, token, method, field string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r)
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		send, err := parseJSONMedia(r.Body, method, field)
		if err != nil {
			writeTelegramError(w, http.StatusBadRequest, http.StatusBadRequest, err.Error(), 0)
			return
		}
		s.respondSend(w, token, send)
		return
	}
	send, err := parseMultipartMedia(r, method, field)
	if err != nil {
		writeTelegramError(w, http.StatusBadRequest, http.StatusBadRequest, err.Error(), 0)
		return
	}
	s.respondSend(w, token, send)
}

func (s *server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	token, filePath, ok := parseFilePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r)
		return
	}
	file, ok := s.fileByPath(token, filePath)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if file.contentType != "" {
		w.Header().Set("Content-Type", file.contentType)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file.data)
}

func (s *server) respondSend(w http.ResponseWriter, token string, send telegramSend) {
	if send.method == "" {
		writeTelegramError(w, http.StatusBadRequest, http.StatusBadRequest, "method is required", 0)
		return
	}
	if send.chatID == 0 {
		writeTelegramError(w, http.StatusBadRequest, http.StatusBadRequest, "chat_id is required", 0)
		return
	}
	if sendErr, ok := s.popSendError(token, send.method); ok {
		send.statusCode = sendErr.statusCode
		send.errorCode = sendErr.errorCode
		send.description = sendErr.description
		send.retryAfter = sendErr.retryAfter
		s.recordSend(token, send)
		writeTelegramError(w, sendErr.statusCode, sendErr.errorCode, sendErr.description, sendErr.retryAfter)
		return
	}
	send.statusCode = http.StatusOK
	s.recordSend(token, send)
	writeTelegramOK(w, map[string]any{"message_id": 1})
}

func (s *server) enqueueUpdates(token string, updates []telegram.Update) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureToken(token)
	state.updates = append(state.updates, updates...)
}

func (s *server) resetState(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		s.tokens = make(map[string]*tokenState)
		return
	}
	state, ok := s.tokens[token]
	if !ok {
		return
	}
	state.updates = nil
	state.files = make(map[string]telegramFile)
	state.sends = nil
	state.sendErrors = nil
}

func (s *server) setFile(token string, file telegramFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureToken(token)
	state.files[file.fileID] = file
}

func (s *server) enqueueSendError(token string, err sendError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureToken(token)
	state.sendErrors = append(state.sendErrors, err)
}

func (s *server) consumeUpdates(token string, offset int64) []telegram.Update {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureToken(token)
	updates := make([]telegram.Update, 0, len(state.updates))
	for _, update := range state.updates {
		if offset == 0 || update.UpdateID >= offset {
			updates = append(updates, update)
		}
	}
	state.updates = nil
	return updates
}

func (s *server) fileByID(token, fileID string) (telegramFile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureToken(token)
	file, ok := state.files[fileID]
	return file, ok
}

func (s *server) fileByPath(token, filePath string) (telegramFile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureToken(token)
	for _, file := range state.files {
		if file.filePath == filePath {
			return file, true
		}
	}
	return telegramFile{}, false
}

func (s *server) sentMessages(token string) []sentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureToken(token)
	messages := make([]sentMessage, 0, len(state.sends))
	for _, send := range state.sends {
		msg := sentMessage{
			Method:            send.method,
			ChatID:            send.chatID,
			Text:              send.text,
			URL:               send.url,
			Filename:          send.filename,
			ContentType:       send.contentType,
			StatusCode:        send.statusCode,
			ErrorCode:         send.errorCode,
			Description:       send.description,
			RetryAfterSeconds: int(send.retryAfter.Seconds()),
		}
		if len(send.data) > 0 {
			msg.DataBase64 = base64.StdEncoding.EncodeToString(send.data)
		}
		messages = append(messages, msg)
	}
	return messages
}

func (s *server) recordSend(token string, send telegramSend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureToken(token)
	state.sends = append(state.sends, send)
}

func (s *server) popSendError(token, method string) (sendError, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureToken(token)
	for idx, err := range state.sendErrors {
		if err.method == "" || err.method == method {
			state.sendErrors = append(state.sendErrors[:idx], state.sendErrors[idx+1:]...)
			return err, true
		}
	}
	return sendError{}, false
}

func (s *server) ensureToken(token string) *tokenState {
	state, ok := s.tokens[token]
	if !ok {
		state = &tokenState{files: make(map[string]telegramFile)}
		s.tokens[token] = state
	}
	return state
}

func parseBotPath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, "/bot") {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, "/bot")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func parseFilePath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, "/file/bot") {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, "/file/bot")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func parseJSONMedia(body io.Reader, method, field string) (telegramSend, error) {
	var payload map[string]any
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return telegramSend{}, err
	}
	chatValue, ok := payload["chat_id"]
	if !ok {
		return telegramSend{}, fmt.Errorf("chat_id is required")
	}
	chatID, err := floatToInt64(chatValue)
	if err != nil {
		return telegramSend{}, err
	}
	urlValue, _ := payload[field].(string)
	return telegramSend{method: method, chatID: chatID, url: urlValue}, nil
}

func parseMultipartMedia(r *http.Request, method, field string) (telegramSend, error) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		return telegramSend{}, err
	}
	chatIDValue := r.FormValue("chat_id")
	if chatIDValue == "" {
		return telegramSend{}, fmt.Errorf("chat_id is required")
	}
	chatID, err := strconv.ParseInt(chatIDValue, 10, 64)
	if err != nil {
		return telegramSend{}, err
	}
	file, header, err := r.FormFile(field)
	if err != nil {
		return telegramSend{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return telegramSend{}, err
	}
	return telegramSend{
		method:      method,
		chatID:      chatID,
		filename:    header.Filename,
		contentType: header.Header.Get("Content-Type"),
		data:        data,
	}, nil
}

func floatToInt64(value any) (int64, error) {
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("invalid chat_id type")
	}
	return int64(number), nil
}

func decodeJSON(body io.Reader, target any) error {
	return json.NewDecoder(body).Decode(target)
}

func writeTelegramOK(w http.ResponseWriter, result any) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func writeTelegramError(w http.ResponseWriter, status, errorCode int, description string, retryAfter time.Duration) {
	payload := map[string]any{
		"ok":          false,
		"error_code":  errorCode,
		"description": description,
	}
	if retryAfter > 0 {
		payload["parameters"] = map[string]any{"retry_after": int(retryAfter.Seconds())}
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
	}
	writeJSON(w, status, payload)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func errorResponse(err error) map[string]string {
	return map[string]string{"error": err.Error()}
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	http.Error(w, fmt.Sprintf("method %s not allowed", r.Method), http.StatusMethodNotAllowed)
}
