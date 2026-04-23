package connector

import (
	"fmt"
	"mime"
	"path"
	"strings"
)

const photoFallbackContentType = "image/jpeg"

var allowedContentTypePrefixes = []string{
	"image/",
	"text/",
	"video/",
	"audio/",
	"application/pdf",
	"application/msword",
	"application/vnd.openxmlformats-officedocument",
	"application/vnd.ms-excel",
	"application/vnd.ms-powerpoint",
	"application/json",
	"application/xml",
	"application/zip",
	"application/gzip",
	"application/x-tar",
}

var contentTypeAliases = map[string]string{
	"application/x-zip-compressed": "application/zip",
	"application/x-gzip":           "application/gzip",
}

type contentTypeCandidate struct {
	source     string
	raw        string
	normalized string
}

func buildContentTypeCandidates(updateMime, sniffed, extension, header string) []contentTypeCandidate {
	return []contentTypeCandidate{
		newContentTypeCandidate("telegram_mime", updateMime),
		newContentTypeCandidate("sniffed", sniffed),
		newContentTypeCandidate("extension", extension),
		newContentTypeCandidate("header", header),
	}
}

func newContentTypeCandidate(source, raw string) contentTypeCandidate {
	trimmed := strings.TrimSpace(raw)
	normalized, ok := normalizeContentType(trimmed)
	if !ok {
		normalized = ""
	}
	return contentTypeCandidate{source: source, raw: trimmed, normalized: normalized}
}

func normalizeContentType(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	mediaType, _, err := mime.ParseMediaType(trimmed)
	if err != nil {
		return "", false
	}
	mediaType = strings.ToLower(mediaType)
	if alias, ok := contentTypeAliases[mediaType]; ok {
		mediaType = alias
	}
	return mediaType, true
}

func selectAllowedContentTypes(candidates []contentTypeCandidate, requireImage bool) []string {
	allowed := make([]string, 0, len(candidates))
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		contentType := candidate.normalized
		if contentType == "" {
			continue
		}
		if isGenericContentType(contentType) {
			continue
		}
		if requireImage && !strings.HasPrefix(contentType, "image/") {
			continue
		}
		if !isAllowedContentType(contentType) {
			continue
		}
		if _, ok := seen[contentType]; ok {
			continue
		}
		seen[contentType] = struct{}{}
		allowed = append(allowed, contentType)
	}
	if requireImage && len(allowed) == 0 {
		allowed = append(allowed, photoFallbackContentType)
	}
	return allowed
}

func isAllowedContentType(contentType string) bool {
	for _, prefix := range allowedContentTypePrefixes {
		if strings.HasPrefix(contentType, prefix) {
			return true
		}
	}
	return false
}

func isGenericContentType(contentType string) bool {
	return contentType == "application/octet-stream"
}

func formatContentTypeCandidates(candidates []contentTypeCandidate) string {
	parts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		raw := strings.TrimSpace(candidate.raw)
		normalized := candidate.normalized
		if raw == "" && normalized == "" {
			continue
		}
		value := normalized
		if normalized == "" {
			value = fmt.Sprintf("invalid(%s)", raw)
		} else if raw != "" && strings.ToLower(raw) != normalized {
			value = fmt.Sprintf("%s (%s)", normalized, raw)
		}
		if candidate.source != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", candidate.source, value))
		} else {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, ", ")
}

func extensionContentType(filePath, filename string) string {
	extension := path.Ext(filePath)
	if extension == "" {
		extension = path.Ext(filename)
	}
	if extension == "" {
		return ""
	}
	return mime.TypeByExtension(strings.ToLower(extension))
}
