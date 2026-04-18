package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

type APIError struct {
	StatusCode  int
	ErrorCode   int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	detail := e.Description
	if detail == "" {
		detail = "telegram api error"
	}
	return fmt.Sprintf("telegram api error: status=%d code=%d desc=%s", e.StatusCode, e.ErrorCode, detail)
}

func (e *APIError) IsRateLimit() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.ErrorCode == http.StatusTooManyRequests
}

func (e *APIError) IsBlocked() bool {
	return e.StatusCode == http.StatusForbidden
}

func (e *APIError) IsTransient() bool {
	return e.StatusCode >= http.StatusInternalServerError || e.StatusCode == 0
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64     `json:"message_id"`
	From      *User     `json:"from"`
	Chat      Chat      `json:"chat"`
	Date      int64     `json:"date"`
	Text      string    `json:"text"`
	Caption   string    `json:"caption"`
	Photo     []Photo   `json:"photo"`
	Document  *Document `json:"document"`
	Audio     *Audio    `json:"audio"`
	Video     *Video    `json:"video"`
	Voice     *Voice    `json:"voice"`
	Sticker   *Sticker  `json:"sticker"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
}

type Photo struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	Width    int64  `json:"width"`
	Height   int64  `json:"height"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type Audio struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type Video struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type Voice struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type Sticker struct {
	FileID string `json:"file_id"`
	Emoji  string `json:"emoji"`
}

type File struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

type apiResponse struct {
	Ok          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func NewClient(token, baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{token: token, baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient}
}

func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	params := map[string]any{
		"timeout": int(timeout.Seconds()),
		"limit":   100,
	}
	if offset > 0 {
		params["offset"] = offset
	}
	var updates []Update
	if err := c.doJSON(ctx, "getUpdates", params, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	params := map[string]any{"file_id": fileID}
	var file File
	if err := c.doJSON(ctx, "getFile", params, &file); err != nil {
		return File{}, err
	}
	if file.FilePath == "" {
		return File{}, fmt.Errorf("get file: empty file_path")
	}
	return file, nil
}

func (c *Client) DownloadFile(ctx context.Context, filePath string) ([]byte, string, error) {
	if filePath == "" {
		return nil, "", fmt.Errorf("download file: empty file path")
	}
	endpoint, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, "", fmt.Errorf("download file: invalid base url: %w", err)
	}
	endpoint.Path = path.Join(endpoint.Path, "file", "bot"+c.token, filePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, "", fmt.Errorf("download file: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("download file: status %d: %s", resp.StatusCode, string(body))
	}
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("download file: %w", err)
	}
	contentType := resp.Header.Get("Content-Type")
	return payload, contentType, nil
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	return c.doJSON(ctx, "sendMessage", payload, nil)
}

func (c *Client) SendPhotoURL(ctx context.Context, chatID int64, url string) error {
	payload := map[string]any{
		"chat_id": chatID,
		"photo":   url,
	}
	return c.doJSON(ctx, "sendPhoto", payload, nil)
}

func (c *Client) SendDocumentURL(ctx context.Context, chatID int64, url string) error {
	payload := map[string]any{
		"chat_id":  chatID,
		"document": url,
	}
	return c.doJSON(ctx, "sendDocument", payload, nil)
}

func (c *Client) SendFile(ctx context.Context, method, fieldName string, chatID int64, filename, contentType string, data []byte) error {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return fmt.Errorf("build multipart: %w", err)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, filename))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("build multipart file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("write multipart file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart: %w", err)
	}

	req, err := c.newRequest(ctx, method, &buf, writer.FormDataContentType())
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.handleResponse(resp, nil)
}

func (c *Client) newRequest(ctx context.Context, method string, body io.Reader, contentType string) (*http.Request, error) {
	endpoint, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base url: %w", err)
	}
	endpoint.Path = path.Join(endpoint.Path, "bot"+c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

func (c *Client) doJSON(ctx context.Context, method string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := c.newRequest(ctx, method, bytes.NewReader(body), "application/json")
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return c.handleResponse(resp, out)
}

func (c *Client) handleResponse(resp *http.Response, out any) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	var apiResp apiResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !apiResp.Ok || resp.StatusCode != http.StatusOK {
		retryAfter := time.Duration(0)
		if header := resp.Header.Get("Retry-After"); header != "" {
			if seconds, err := strconv.Atoi(header); err == nil {
				retryAfter = time.Duration(seconds) * time.Second
			}
		}
		if retryAfter == 0 && apiResp.Parameters != nil && apiResp.Parameters.RetryAfter > 0 {
			retryAfter = time.Duration(apiResp.Parameters.RetryAfter) * time.Second
		}
		return &APIError{
			StatusCode:  resp.StatusCode,
			ErrorCode:   apiResp.ErrorCode,
			Description: apiResp.Description,
			RetryAfter:  retryAfter,
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(apiResp.Result, out); err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	return nil
}
