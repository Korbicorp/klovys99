package aiworkspace

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	aiWorkspaceDirEnv = "KLOVYS99_AI_WORKSPACE_DIR"
	aiWorkspaceKeyEnv = "KLOVYS99_AI_WORKSPACE_KEY"
)

var (
	ansiPattern  = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07]*\x07|[()#][0-9A-Za-z])`)
	urlPattern   = regexp.MustCompile(`https://claude\.com/cai/oauth/authorize\?[^\s]+`)
	tokenPattern = regexp.MustCompile(`sk-ant-oat01-[A-Za-z0-9_\-]+`)
)

type ClaudeOAuthSubmitRequest struct {
	Code string `json:"code"`
}

type ClaudeOAuthStartResponse struct {
	Linked           bool   `json:"linked"`
	Pending          bool   `json:"pending"`
	Method           string `json:"method"`
	AuthorizationURL string `json:"authorization_url,omitempty"`
}

type ClaudeOAuthStatusResponse struct {
	Linked           bool   `json:"linked"`
	Pending          bool   `json:"pending"`
	Method           string `json:"method"`
	AuthorizationURL string `json:"authorization_url,omitempty"`
}

type claudeRunner interface {
	StartSetup(configDir string) (*oauthLinkSession, error)
	RunOAuthPrompt(ctx context.Context, configDir string, token string, model string, systemPrompt string, prompt string) (string, error)
}

type liveClaudeRunner struct{}

type oauthLinkSession struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{}
	waitErr  error
	logOnce  sync.Once
	outputMu sync.Mutex
	output   bytes.Buffer
	url      string
	urlMu    sync.Mutex
}

type claudeOAuthManager struct {
	mu       sync.Mutex
	stateDir string
	runner   claudeRunner
	session  *oauthLinkSession
}

func newClaudeOAuthManager(stateDir string, runner claudeRunner) *claudeOAuthManager {
	return &claudeOAuthManager{
		stateDir: stateDir,
		runner:   runner,
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

func encryptionSecret() string {
	return strings.TrimSpace(os.Getenv(aiWorkspaceKeyEnv))
}

func (m *claudeOAuthManager) status() ClaudeOAuthStatusResponse {
	m.mu.Lock()
	defer m.mu.Unlock()

	response := ClaudeOAuthStatusResponse{
		Linked: tokenFileExists(m.tokenPath()),
		Method: "oauth_token",
	}
	if m.session != nil && m.session.isRunning() {
		response.Pending = true
		response.AuthorizationURL = m.session.authorizationURL()
	}
	return response
}

func (m *claudeOAuthManager) availability() (bool, string) {
	if strings.TrimSpace(encryptionSecret()) == "" {
		return false, fmt.Sprintf("Set %s before using Claude OAuth", aiWorkspaceKeyEnv)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return false, "Claude CLI is not installed or not available in PATH"
	}
	if tokenFileExists(m.tokenPath()) {
		return true, ""
	}
	if _, err := exec.LookPath("script"); err != nil {
		return false, "`script` command is required to start Claude OAuth"
	}
	return true, ""
}

func (m *claudeOAuthManager) start() (ClaudeOAuthStartResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if available, message := m.availability(); !available {
		return ClaudeOAuthStartResponse{}, &apiError{StatusCode: http.StatusBadRequest, Message: message}
	}
	if m.session != nil && m.session.isRunning() {
		return ClaudeOAuthStartResponse{
			Linked:           tokenFileExists(m.tokenPath()),
			Pending:          true,
			Method:           "oauth_token",
			AuthorizationURL: m.session.authorizationURL(),
		}, nil
	}
	session, err := m.runner.StartSetup(m.configDir())
	if err != nil {
		return ClaudeOAuthStartResponse{}, normalizeCLIError("Claude OAuth start failed", err)
	}
	url, err := session.waitForAuthorizationURL(20 * time.Second)
	if err != nil {
		_ = session.terminate()
		return ClaudeOAuthStartResponse{}, normalizeCLIError("Claude OAuth start failed", err)
	}
	m.session = session
	return ClaudeOAuthStartResponse{
		Linked:           tokenFileExists(m.tokenPath()),
		Pending:          true,
		Method:           "oauth_token",
		AuthorizationURL: url,
	}, nil
}

func (m *claudeOAuthManager) submit(ctx context.Context, request ClaudeOAuthSubmitRequest) (ClaudeOAuthStatusResponse, error) {
	code := strings.TrimSpace(request.Code)
	if code == "" {
		return ClaudeOAuthStatusResponse{}, &apiError{StatusCode: http.StatusBadRequest, Message: "OAuth code is required"}
	}
	m.mu.Lock()
	session := m.session
	m.mu.Unlock()
	if session == nil || !session.isRunning() {
		return ClaudeOAuthStatusResponse{}, &apiError{StatusCode: http.StatusBadRequest, Message: "No Claude OAuth link is in progress"}
	}
	if err := session.writeCode(code); err != nil {
		return ClaudeOAuthStatusResponse{}, normalizeCLIError("Claude OAuth submit failed", err)
	}
	token, err := session.waitForToken(ctx, 25*time.Second)
	_ = session.terminate()
	m.mu.Lock()
	if m.session == session {
		m.session = nil
	}
	m.mu.Unlock()
	if err != nil {
		return ClaudeOAuthStatusResponse{}, normalizeCLIError("Claude OAuth submit failed", err)
	}
	if err := writeEncryptedFile(m.tokenPath(), token); err != nil {
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
	if err := os.Remove(m.tokenPath()); err != nil && !os.IsNotExist(err) {
		return &apiError{StatusCode: http.StatusInternalServerError, Message: fmt.Sprintf("remove Claude OAuth token: %v", err)}
	}
	return nil
}

func (m *claudeOAuthManager) loadToken() (string, error) {
	token, err := readEncryptedFile(m.tokenPath())
	if err != nil {
		return "", &apiError{StatusCode: http.StatusBadRequest, Message: fmt.Sprintf("load Claude OAuth token: %v", err)}
	}
	return token, nil
}

func (m *claudeOAuthManager) tokenPath() string {
	return filepath.Join(m.stateDir, "claude", "oauth_token.enc")
}

func (m *claudeOAuthManager) configDir() string {
	return filepath.Join(m.stateDir, "claude", "config")
}

func (r *liveClaudeRunner) StartSetup(configDir string) (*oauthLinkSession, error) {
	if _, err := exec.LookPath("script"); err != nil {
		return nil, fmt.Errorf("`script` command is required to drive Claude OAuth: %w", err)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("`claude` CLI is required for Claude OAuth: %w", err)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, err
	}
	cmd := exec.Command("script", "-q", "/dev/null", "claude", "setup-token")
	cmd.Env = claudeEnv(configDir, "")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	session := &oauthLinkSession{
		cmd:   cmd,
		stdin: stdin,
		done:  make(chan struct{}),
	}
	go session.capture(stdout)
	go session.capture(stderr)
	go func() {
		session.waitErr = cmd.Wait()
		session.logTerminalOutput("Claude OAuth terminal session completed")
		close(session.done)
	}()
	return session, nil
}

func (r *liveClaudeRunner) RunOAuthPrompt(ctx context.Context, configDir string, token string, model string, systemPrompt string, prompt string) (string, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return "", fmt.Errorf("`claude` CLI is required for Claude OAuth prompts: %w", err)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(
		ctx,
		"claude", "-p",
		"--tools", "",
		"--output-format", "json",
		"--model", model,
		"--system-prompt", systemPrompt,
		"--no-session-persistence",
		prompt,
	)
	cmd.Env = claudeEnv(configDir, token)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", errors.New(strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	var payload struct {
		Result    string `json:"result"`
		IsError   bool   `json:"is_error"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return "", fmt.Errorf("unreadable Claude CLI response")
	}
	if payload.IsError {
		return "", errors.New(payload.Result)
	}
	return payload.Result, nil
}

func claudeEnv(configDir string, token string) []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+3)
	for _, entry := range env {
		if strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") || strings.HasPrefix(entry, "CLAUDE_CODE_OAUTH_TOKEN=") || strings.HasPrefix(entry, "ANTHROPIC_API_KEY=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, "CLAUDE_CONFIG_DIR="+configDir)
	if token != "" {
		filtered = append(filtered, "CLAUDE_CODE_OAUTH_TOKEN="+token)
	}
	filtered = append(filtered, "TERM=xterm-256color")
	return filtered
}

func (s *oauthLinkSession) capture(stream io.ReadCloser) {
	defer stream.Close()
	buffer := make([]byte, 4096)
	for {
		count, err := stream.Read(buffer)
		if count > 0 {
			chunk := ansiPattern.ReplaceAllString(string(buffer[:count]), "")
			s.outputMu.Lock()
			s.output.WriteString(chunk)
			match := urlPattern.FindString(s.output.String())
			if match != "" {
				s.urlMu.Lock()
				s.url = match
				s.urlMu.Unlock()
			}
			s.outputMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *oauthLinkSession) authorizationURL() string {
	s.urlMu.Lock()
	defer s.urlMu.Unlock()
	return s.url
}

func (s *oauthLinkSession) waitForAuthorizationURL(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if url := s.authorizationURL(); url != "" {
			return url, nil
		}
		if !s.isRunning() {
			return "", errors.New(cleanCLIError(s.snapshot()))
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("timed out while waiting for Claude authorization URL")
}

func (s *oauthLinkSession) writeCode(code string) error {
	if s.stdin == nil {
		return fmt.Errorf("Claude OAuth session is not writable")
	}
	_, err := io.WriteString(s.stdin, code+"\n")
	return err
}

func (s *oauthLinkSession) waitForToken(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", fmt.Errorf("timed out while waiting for Claude OAuth token")
		case <-ticker.C:
			if match := tokenPattern.FindString(s.snapshot()); match != "" {
				return match, nil
			}
			if !s.isRunning() {
				buffer := strings.ToLower(s.snapshot())
				if strings.Contains(buffer, "invalid") || strings.Contains(buffer, "expired") || strings.Contains(buffer, "error") {
					return "", fmt.Errorf("OAuth code invalid or expired")
				}
				return "", errors.New(cleanCLIError(s.snapshot()))
			}
		}
	}
}

func (s *oauthLinkSession) snapshot() string {
	s.outputMu.Lock()
	defer s.outputMu.Unlock()
	str := s.output.String()
	log.Info().Str("cli", str).Msg("cli resp")
	return strings.ReplaceAll(str, "\r", "")
}

func (s *oauthLinkSession) logTerminalOutput(message string) {
	s.logOnce.Do(func() {
		log.Info().
			Str("terminal_output", s.snapshot()).
			Msg(message)
	})
}

func (s *oauthLinkSession) terminate() error {
	if s == nil {
		return nil
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	if s.isRunning() {
		if err := s.cmd.Process.Kill(); err != nil && !strings.Contains(err.Error(), "finished") {
			return err
		}
	}
	<-s.done
	return nil
}

func (s *oauthLinkSession) isRunning() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func writeEncryptedFile(path string, value string) error {
	secret := encryptionSecret()
	if secret == "" {
		return fmt.Errorf("missing encryption secret")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	key := deriveKey(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	payload := gcm.Seal(nonce, nonce, []byte(value), nil)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func readEncryptedFile(path string) (string, error) {
	secret := encryptionSecret()
	if secret == "" {
		return "", fmt.Errorf("missing encryption secret")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := deriveKey(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(payload) < gcm.NonceSize() {
		return "", fmt.Errorf("encrypted token payload is invalid")
	}
	nonce := payload[:gcm.NonceSize()]
	ciphertext := payload[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func deriveKey(secret string) [32]byte {
	return sha256.Sum256([]byte(secret))
}

func tokenFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func cleanCLIError(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown Claude CLI error"
	}
	return raw
}

func normalizeCLIError(prefix string, err error) error {
	if err == nil {
		return nil
	}
	if apiErr, ok := err.(*apiError); ok {
		return apiErr
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "unknown error"
	}
	statusCode := http.StatusBadGateway
	if strings.Contains(strings.ToLower(message), "required") || strings.Contains(strings.ToLower(message), "invalid") || strings.Contains(strings.ToLower(message), "expired") || strings.Contains(strings.ToLower(message), "missing") {
		statusCode = http.StatusBadRequest
	}
	return &apiError{StatusCode: statusCode, Message: fmt.Sprintf("%s: %s", prefix, message)}
}
