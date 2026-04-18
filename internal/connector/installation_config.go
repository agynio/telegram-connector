package connector

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
)

func parseInstallationConfig(config *structpb.Struct) (string, uuid.UUID, error) {
	if config == nil {
		return "", uuid.Nil, fmt.Errorf("installation configuration is missing")
	}
	fields := config.GetFields()
	botField, ok := fields["bot_token"]
	if !ok {
		return "", uuid.Nil, fmt.Errorf("installation configuration missing bot_token")
	}
	botToken := strings.TrimSpace(botField.GetStringValue())
	if botToken == "" {
		return "", uuid.Nil, fmt.Errorf("installation configuration bot_token is empty")
	}
	agentField, ok := fields["agent_id"]
	if !ok {
		return "", uuid.Nil, fmt.Errorf("installation configuration missing agent_id")
	}
	agentValue := strings.TrimSpace(agentField.GetStringValue())
	if agentValue == "" {
		return "", uuid.Nil, fmt.Errorf("installation configuration agent_id is empty")
	}
	agentID, err := uuid.Parse(agentValue)
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("installation configuration agent_id invalid: %w", err)
	}
	return botToken, agentID, nil
}
