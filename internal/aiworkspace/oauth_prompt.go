package aiworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type claudeOAuthConversationService interface {
	RunPrompt(ctx context.Context, configDir string, token string, sessionID string, resume bool, model string, systemPrompt string, prompt string) (claudeOAuthPromptResult, error)
}

type liveClaudeConversationService struct{}

type claudeRunnerConversationAdapter struct {
	runner claudeRunner
}

func (m *claudeOAuthManager) runPrompt(ctx context.Context, conversation ConversationDetail, model string, systemPrompt string, prompt string) (providerCallResult, error) {
	token, err := m.loadToken()
	if err != nil {
		return providerCallResult{}, err
	}
	sessionID := strings.TrimSpace(conversation.ProviderSessionID)
	resume := sessionID != ""
	if sessionID == "" {
		sessionID = claudeConversationSessionID(conversation.ID)
	}
	result, err := m.conversation.RunPrompt(ctx, m.configDir(), token, sessionID, resume, model, systemPrompt, prompt)
	if err != nil {
		return providerCallResult{}, normalizeCLIError("Claude request failed", err)
	}
	providerSessionID := strings.TrimSpace(result.SessionID)
	if providerSessionID == "" {
		providerSessionID = sessionID
	}
	return providerCallResult{
		ResponseText:      result.Text,
		ProviderSessionID: providerSessionID,
	}, nil
}

func (s *liveClaudeConversationService) RunPrompt(ctx context.Context, configDir string, token string, sessionID string, resume bool, model string, systemPrompt string, prompt string) (claudeOAuthPromptResult, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return claudeOAuthPromptResult{}, fmt.Errorf("`claude` CLI is required for Claude OAuth prompts: %w", err)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return claudeOAuthPromptResult{}, err
	}
	args := []string{
		"-p",
		"--tools", "",
		"--output-format", "json",
		"--model", model,
		"--system-prompt", systemPrompt,
	}
	if resume {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Env = claudeEnv(configDir, token)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			logClaudePromptFailure(model, prompt, exitErr.ExitCode(), stdout.String(), stderr.String())
			return claudeOAuthPromptResult{}, errors.New(cleanCLIError(firstNonEmpty(stderr.String(), stdout.String())))
		}
		logClaudePromptFailure(model, prompt, -1, stdout.String(), stderr.String())
		return claudeOAuthPromptResult{}, err
	}
	output := stdout.Bytes()
	var payload struct {
		Result    string `json:"result"`
		IsError   bool   `json:"is_error"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return claudeOAuthPromptResult{}, fmt.Errorf("unreadable Claude CLI response")
	}
	if payload.IsError {
		return claudeOAuthPromptResult{}, errors.New(cleanCLIError(payload.Result))
	}
	return claudeOAuthPromptResult{
		Text:      payload.Result,
		SessionID: strings.TrimSpace(payload.SessionID),
	}, nil
}

func (a claudeRunnerConversationAdapter) RunPrompt(ctx context.Context, configDir string, token string, sessionID string, resume bool, model string, systemPrompt string, prompt string) (claudeOAuthPromptResult, error) {
	return a.runner.RunOAuthPrompt(ctx, configDir, token, sessionID, resume, model, systemPrompt, prompt)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
