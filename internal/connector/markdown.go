package connector

import (
	"regexp"
	"strings"
)

var inlineImagePattern = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

func extractInlineImages(body string) (string, []string) {
	if body == "" {
		return "", nil
	}
	var images []string
	cleaned := inlineImagePattern.ReplaceAllStringFunc(body, func(match string) string {
		parts := inlineImagePattern.FindStringSubmatch(match)
		if len(parts) > 1 {
			images = append(images, strings.TrimSpace(parts[1]))
		}
		return ""
	})
	return strings.TrimSpace(cleaned), images
}
