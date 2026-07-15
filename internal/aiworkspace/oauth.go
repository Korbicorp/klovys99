package aiworkspace

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Korbicorp/klovys99/internal/credential"
	"github.com/rs/zerolog/log"
)

const (
	aiWorkspaceDirEnv = "KLOVYS99_AI_WORKSPACE_DIR"
)

type ClaudeOAuthSubmitRequest struct {
	Code string `json:"code"`
}

type ClaudeOAuthStartResponse struct {
	Linked           bool   `json:"linked"`
	Pending          bool   `json:"pending"`
	Method           string `json:"method"`
	AuthorizationURL string `json:"authorization_url,omitempty"`
	RequiresCode     bool   `json:"requires_code,omitempty"`
}

type ClaudeOAuthStatusResponse struct {
	Linked           bool   `json:"linked"`
	Pending          bool   `json:"pending"`
	Method           string `json:"method"`
	AuthorizationURL string `json:"authorization_url,omitempty"`
	RequiresCode     bool   `json:"requires_code,omitempty"`
}

type claudeRunner interface {
	StartSetup(configDir string) (*oauthLinkSession, error)
	RunOAuthPrompt(ctx context.Context, configDir string, token string, sessionID string, resume bool, model string, systemPrompt string, prompt string) (claudeOAuthPromptResult, error)
}

type claudeOAuthManager struct {
	mu           sync.Mutex
	stateDir     string
	creds        *credential.Store
	terminal     claudeOAuthTerminalService
	conversation claudeOAuthConversationService
	session      *oauthLinkSession
}

func newClaudeOAuthManager(stateDir string, creds *credential.Store, runner claudeRunner) *claudeOAuthManager {
	terminal := claudeOAuthTerminalService(&liveClaudeTerminalService{})
	conversation := claudeOAuthConversationService(&liveClaudeConversationService{})
	if runner != nil {
		terminal = claudeRunnerTerminalAdapter{runner: runner}
		conversation = claudeRunnerConversationAdapter{runner: runner}
	}
	return &claudeOAuthManager{
		stateDir:     stateDir,
		creds:        creds,
		terminal:     terminal,
		conversation: conversation,
	}
}

func defaultWorkspaceStateDir() string {
	if configured := strings.TrimSpace(os.Getenv(aiWorkspaceDirEnv)); configured != "" {
		return configured
	}
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".", ".klovys99-ai-workspace")
	}
	return filepath.Join(userConfigDir, "klovys99", "ai-workspace")
}

func (m *claudeOAuthManager) status() ClaudeOAuthStatusResponse {
	m.mu.Lock()
	if err := m.finalizeSessionLocked(); err != nil {
		m.mu.Unlock()
		return ClaudeOAuthStatusResponse{
			Linked: false,
			Method: "oauth_token",
		}
	}
	defer m.mu.Unlock()

	response := ClaudeOAuthStatusResponse{
		Linked: m.hasToken(),
		Method: "oauth_token",
	}
	if m.session != nil && m.session.isRunning() {
		response.Pending = true
		response.AuthorizationURL = m.session.authorizationURL()
		response.RequiresCode = m.session.requiresCode()
	}
	return response
}

func (m *claudeOAuthManager) availability() (bool, string) {
	if !credential.HasSecret() {
		return false, fmt.Sprintf("Set %s before using Claude OAuth", credential.SecretEnv)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return false, "Claude CLI is not installed or not available in PATH"
	}
	if m.hasToken() {
		return true, ""
	}
	if _, err := exec.LookPath("script"); err != nil {
		return false, "`script` command is required to start Claude OAuth"
	}
	return true, ""
}

func (m *claudeOAuthManager) start() (ClaudeOAuthStartResponse, error) {
	m.mu.Lock()
	if available, message := m.availability(); !available {
		m.mu.Unlock()
		return ClaudeOAuthStartResponse{}, &apiError{StatusCode: http.StatusBadRequest, Message: message}
	}
	if err := m.finalizeSessionLocked(); err != nil {
		m.mu.Unlock()
		return ClaudeOAuthStartResponse{}, normalizeCLIError("Claude OAuth start failed", err)
	}
	if m.session != nil && m.session.isRunning() {
		response := ClaudeOAuthStartResponse{
			Linked:           m.hasToken(),
			Pending:          true,
			Method:           "oauth_token",
			AuthorizationURL: m.session.authorizationURL(),
			RequiresCode:     m.session.requiresCode(),
		}
		m.mu.Unlock()
		return response, nil
	}
	session, err := m.terminal.StartSetup(m.configDir())
	if err != nil {
		m.mu.Unlock()
		return ClaudeOAuthStartResponse{}, normalizeCLIError("Claude OAuth start failed", err)
	}
	url, err := session.waitForAuthorizationURL(20 * time.Second)
	if err != nil {
		m.mu.Unlock()
		_ = session.terminate()
		return ClaudeOAuthStartResponse{}, normalizeCLIError("Claude OAuth start failed", err)
	}
	m.session = session
	go m.watchSession(session)
	response := ClaudeOAuthStartResponse{
		Linked:           m.hasToken(),
		Pending:          true,
		Method:           "oauth_token",
		AuthorizationURL: url,
		RequiresCode:     session.requiresCode(),
	}
	m.mu.Unlock()
	return response, nil
}

func (m *claudeOAuthManager) submit(ctx context.Context, request ClaudeOAuthSubmitRequest) (ClaudeOAuthStatusResponse, error) {
	code := strings.TrimSpace(request.Code)
	if code == "" {
		return ClaudeOAuthStatusResponse{}, &apiError{StatusCode: http.StatusBadRequest, Message: "OAuth token is required"}
	}
	log.Info().
		Str("component", "claude_oauth").
		Int("submitted_length", len(code)).
		Bool("looks_like_long_lived_token", extractOAuthToken(code) != "").
		Msg("Claude OAuth submit received")
	m.mu.Lock()
	if err := m.finalizeSessionLocked(); err == nil && m.hasToken() {
		m.mu.Unlock()
		return m.status(), nil
	}
	session := m.session
	m.mu.Unlock()
	if session == nil || !session.isRunning() {
		return ClaudeOAuthStatusResponse{}, &apiError{StatusCode: http.StatusBadRequest, Message: "No Claude OAuth link is in progress"}
	}
	if longLivedToken := extractOAuthToken(code); longLivedToken != "" {
		session.setDetectedToken(longLivedToken)
		if err := m.persistSessionToken(session); err != nil {
			return ClaudeOAuthStatusResponse{}, &apiError{StatusCode: http.StatusInternalServerError, Message: fmt.Sprintf("store Claude OAuth token: %v", err)}
		}
		return m.status(), nil
	}
	if !session.requiresCode() {
		return ClaudeOAuthStatusResponse{}, &apiError{StatusCode: http.StatusBadRequest, Message: "Claude OAuth does not require a manual token"}
	}
	if err := session.writeCode(code); err != nil {
		return ClaudeOAuthStatusResponse{}, normalizeCLIError("Claude OAuth submit failed", err)
	}
	log.Info().
		Str("component", "claude_oauth").
		Bool("session_running", session.isRunning()).
		Msg("Claude OAuth token forwarded to CLI session")
	token, err := session.waitForToken(ctx, 25*time.Second)
	if err != nil {
		return ClaudeOAuthStatusResponse{}, normalizeCLIError("Claude OAuth submit failed", err)
	}
	session.setDetectedToken(token)
	if err := m.persistSessionToken(session); err != nil {
		return ClaudeOAuthStatusResponse{}, &apiError{StatusCode: http.StatusInternalServerError, Message: fmt.Sprintf("store Claude OAuth token: %v", err)}
	}
	return m.status(), nil
}

func (m *claudeOAuthManager) cancel() error {
	m.mu.Lock()
	session := m.session
	m.session = nil
	m.mu.Unlock()
	if session == nil {
		return nil
	}
	if err := session.terminate(); err != nil {
		return normalizeCLIError("Claude OAuth cancel failed", err)
	}
	return nil
}

func (m *claudeOAuthManager) unlink() error {
	if err := m.cancel(); err != nil {
		return err
	}
	if m.creds == nil {
		return nil
	}
	if err := m.creds.DeleteOAuthToken("claude", "oauth_token"); err != nil {
		return &apiError{StatusCode: http.StatusInternalServerError, Message: fmt.Sprintf("remove Claude OAuth token: %v", err)}
	}
	return nil
}

func (m *claudeOAuthManager) loadToken() (string, error) {
	if m.creds == nil {
		return "", &apiError{StatusCode: http.StatusBadRequest, Message: "load Claude OAuth token: credential store unavailable"}
	}
	token, err := m.creds.LoadOAuthToken("claude", "oauth_token")
	if err != nil {
		return "", &apiError{StatusCode: http.StatusBadRequest, Message: fmt.Sprintf("load Claude OAuth token: %v", err)}
	}
	return token, nil
}

func (m *claudeOAuthManager) tokenPath() string {
	if m.creds == nil {
		return filepath.Join(m.stateDir, "claude", "oauth_token.enc")
	}
	return m.creds.OAuthTokenPath("claude", "oauth_token")
}

func (m *claudeOAuthManager) configDir() string {
	return filepath.Join(m.stateDir, "claude", "config")
}

func (m *claudeOAuthManager) finalizeSessionLocked() error {
	if m.session == nil {
		return nil
	}
	token := m.session.detectedToken()
	if token == "" {
		if !m.session.isRunning() {
			err := m.session.lastError()
			m.session = nil
			if err != nil {
				return err
			}
		}
		return nil
	}
	session := m.session
	m.mu.Unlock()
	err := m.persistSessionToken(session)
	m.mu.Lock()
	return err
}

func (m *claudeOAuthManager) persistSessionToken(session *oauthLinkSession) error {
	if session == nil {
		return nil
	}
	token := session.detectedToken()
	if token == "" {
		return nil
	}
	if session.isStored() {
		m.mu.Lock()
		if m.session == session {
			m.session = nil
		}
		m.mu.Unlock()
		return nil
	}
	if m.creds == nil {
		session.setFinalError(fmt.Errorf("credential store unavailable"))
		return fmt.Errorf("credential store unavailable")
	}
	if err := m.creds.SaveOAuthToken("claude", "oauth_token", token); err != nil {
		session.setFinalError(err)
		return err
	}
	session.markStored()
	_ = session.terminate()
	m.mu.Lock()
	if m.session == session {
		m.session = nil
	}
	m.mu.Unlock()
	return nil
}

func (m *claudeOAuthManager) watchSession(session *oauthLinkSession) {
	if session == nil {
		return
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if token := session.detectedToken(); token != "" {
			_ = m.persistSessionToken(session)
			return
		}
		if !session.isRunning() {
			return
		}
		<-ticker.C
	}
}

func (m *claudeOAuthManager) hasToken() bool {
	return m != nil && m.creds != nil && m.creds.OAuthTokenExists("claude", "oauth_token")
}
