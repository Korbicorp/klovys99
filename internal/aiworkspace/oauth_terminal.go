package aiworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

var (
	ansiPattern           = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07]*\x07|[()#][0-9A-Za-z])`)
	tokenRedactionPattern = regexp.MustCompile(`sk-ant-oat01-[A-Za-z0-9_\-\s]+`)
	controlPattern        = regexp.MustCompile(`[\x00-\x08\x0B-\x1F\x7F]`)
)

type claudeOAuthPromptResult struct {
	Text      string
	SessionID string
}

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
	stateMu  sync.Mutex
	token    string
	stored   bool
	finalErr error
	started  time.Time
}

type claudeOAuthTerminalService interface {
	StartSetup(configDir string) (*oauthLinkSession, error)
}

type liveClaudeTerminalService struct{}

type claudeRunnerTerminalAdapter struct {
	runner claudeRunner
}

func (s *liveClaudeTerminalService) StartSetup(configDir string) (*oauthLinkSession, error) {
	if _, err := exec.LookPath("script"); err != nil {
		return nil, fmt.Errorf("`script` command is required to drive Claude OAuth: %w", err)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("`claude` CLI is required for Claude OAuth: %w", err)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, err
	}
	cmd := claudeSetupCommand()
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
		cmd:     cmd,
		stdin:   stdin,
		done:    make(chan struct{}),
		started: time.Now().UTC(),
	}
	go session.capture("stdout", stdout)
	go session.capture("stderr", stderr)
	go func() {
		session.waitErr = cmd.Wait()
		session.logTerminalOutput("Claude OAuth terminal session completed")
		close(session.done)
	}()
	return session, nil
}

func (a claudeRunnerTerminalAdapter) StartSetup(configDir string) (*oauthLinkSession, error) {
	return a.runner.StartSetup(configDir)
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

func (s *oauthLinkSession) capture(streamName string, stream io.ReadCloser) {
	defer stream.Close()
	buffer := make([]byte, 4096)
	for {
		count, err := stream.Read(buffer)
		if count > 0 {
			chunk := ansiPattern.ReplaceAllString(string(buffer[:count]), "")
			s.logTerminalChunk(streamName, chunk)
			s.outputMu.Lock()
			s.output.WriteString(chunk)
			snapshot := s.output.String()
			match := extractAuthorizationURL(snapshot)
			if match != "" {
				s.urlMu.Lock()
				s.url = match
				s.urlMu.Unlock()
			}
			if token := extractOAuthToken(snapshot); token != "" {
				s.setDetectedToken(token)
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
	if _, err := io.WriteString(s.stdin, code); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)
	_, err := io.WriteString(s.stdin, "\r\n")
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
			if match := s.detectedToken(); match != "" {
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
	return strings.ReplaceAll(s.output.String(), "\r", "")
}

func (s *oauthLinkSession) requiresCode() bool {
	snapshot := strings.ToLower(s.snapshot())
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(snapshot)
	return strings.Contains(snapshot, "authorization code") ||
		strings.Contains(snapshot, "paste the code") ||
		strings.Contains(snapshot, "enter the code") ||
		strings.Contains(snapshot, "oauth token") ||
		strings.Contains(compact, "pastecodehereifprompted")
}

func (s *oauthLinkSession) detectedToken() string {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.token
}

func (s *oauthLinkSession) setDetectedToken(token string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.token = strings.TrimSpace(token)
}

func (s *oauthLinkSession) isStored() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.stored
}

func (s *oauthLinkSession) markStored() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.stored = true
	s.finalErr = nil
}

func (s *oauthLinkSession) setFinalError(err error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.finalErr = err
}

func (s *oauthLinkSession) lastError() error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.finalErr != nil {
		return s.finalErr
	}
	if s.waitErr != nil {
		return errors.New(cleanCLIError(s.snapshot()))
	}
	return nil
}

func (s *oauthLinkSession) logTerminalOutput(message string) {
	s.logOnce.Do(func() {
		log.Info().
			Str("component", "claude_oauth").
			Str("message", message).
			Str("terminal_output", sanitizeTerminalOutput(s.snapshot())).
			Msg("Claude OAuth terminal session")
	})
}

func (s *oauthLinkSession) logTerminalChunk(streamName string, chunk string) {
	sanitized := strings.TrimSpace(sanitizeTerminalOutput(chunk))
	if sanitized == "" {
		return
	}
	log.Info().
		Str("component", "claude_oauth").
		Str("stream", streamName).
		Str("chunk", sanitized).
		Msg("Claude OAuth CLI output received")
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

func cleanCLIError(raw string) string {
	raw = strings.TrimSpace(sanitizeTerminalOutput(raw))
	if raw == "" {
		return "unknown Claude CLI error"
	}
	return raw
}

func logClaudePromptFailure(model string, prompt string, exitCode int, stdout string, stderr string) {
	event := log.Info().
		Str("component", "claude_oauth").
		Str("model", model).
		Int("prompt_length", len(prompt)).
		Str("stdout", sanitizeTerminalOutput(stdout)).
		Str("stderr", sanitizeTerminalOutput(stderr))
	if exitCode >= 0 {
		event = event.Int("exit_code", exitCode)
	}
	event.Msg("Claude OAuth prompt command failed")
}

func sanitizeTerminalOutput(raw string) string {
	raw = strings.ReplaceAll(raw, "\r", "")
	raw = controlPattern.ReplaceAllString(raw, "")
	return raw
}

func extractOAuthToken(raw string) string {
	if token := extractOAuthTokenFromSuccessMessage(raw); token != "" {
		return token
	}
	lines := strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n")
	for index, line := range lines {
		start := strings.Index(line, "sk-ant-oat01-")
		if start < 0 {
			continue
		}
		token := collectTokenLines(append([]string{line[start:]}, lines[index+1:]...))
		if token != "" {
			return token
		}
	}
	return ""
}

func extractOAuthTokenFromSuccessMessage(raw string) string {
	normalized := strings.ReplaceAll(raw, "\r", "")
	startMarker := "Your OAuth token (valid for 1 year):"
	start := strings.Index(normalized, startMarker)
	if start < 0 {
		return ""
	}
	segment := normalized[start+len(startMarker):]
	end := len(segment)
	for _, marker := range []string{
		"Store this token securely",
		"Use this token by setting:",
		"Use this token by setting",
	} {
		if index := strings.Index(segment, marker); index >= 0 && index < end {
			end = index
		}
	}
	segment = segment[:end]
	tokenStart := strings.Index(segment, "sk-ant-oat01-")
	if tokenStart < 0 {
		return ""
	}
	var builder strings.Builder
	for _, r := range segment[tokenStart:] {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
			continue
		}
		if r == ' ' || r == '\n' || r == '\t' {
			continue
		}
		break
	}
	token := builder.String()
	if !strings.HasPrefix(token, "sk-ant-oat01-") {
		return ""
	}
	return token
}

func extractAuthorizationURL(raw string) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n")
	for index, line := range lines {
		start := strings.Index(line, "https://claude.com/cai/oauth/authorize?")
		if start < 0 {
			continue
		}
		url := collectWrappedURL(append([]string{line[start:]}, lines[index+1:]...))
		if url != "" {
			return url
		}
	}
	return ""
}

func collectWrappedURL(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var builder strings.Builder
	first := sanitizeURLFragment(lines[0], true)
	if first == "" {
		return ""
	}
	builder.WriteString(first)
	for _, line := range lines[1:] {
		fragment := sanitizeURLFragment(line, false)
		if fragment == "" {
			break
		}
		builder.WriteString(fragment)
	}
	url := builder.String()
	if !strings.HasPrefix(url, "https://claude.com/cai/oauth/authorize?") {
		return ""
	}
	return url
}

func sanitizeURLFragment(line string, allowPrefix bool) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case strings.ContainsRune("-._~:/?#[]@!$&'()*+,;=%", r):
			builder.WriteRune(r)
		default:
			return builder.String()
		}
	}
	fragment := builder.String()
	if fragment == "" {
		return ""
	}
	if allowPrefix && !strings.HasPrefix(fragment, "https://claude.com/cai/oauth/authorize?") {
		return ""
	}
	if !allowPrefix && trimmed != fragment {
		return ""
	}
	return fragment
}

func collectTokenLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var builder strings.Builder
	first := sanitizeTokenFragment(lines[0], true)
	if first == "" {
		return ""
	}
	builder.WriteString(first)
	for _, line := range lines[1:] {
		fragment := sanitizeTokenFragment(line, false)
		if fragment == "" {
			break
		}
		builder.WriteString(fragment)
	}
	token := trimClaudeTerminalTokenSuffix(builder.String())
	if !strings.HasPrefix(token, "sk-ant-oat01-") {
		return ""
	}
	return token
}

func trimClaudeTerminalTokenSuffix(token string) string {
	if token == "" {
		return ""
	}
	end := len(token)
	for _, marker := range []string{
		"Store",
		"Storethistokensecurely",
		"Youwontbeabletoseeitagain",
		"Use",
		"Usethistokenbysetting",
		"CLAUDE_CODE_OAUTH_TOKEN",
	} {
		if index := strings.Index(token, marker); index >= 0 && index < end {
			end = index
		}
	}
	return token[:end]
}

func sanitizeTokenFragment(line string, allowPrefix bool) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
			continue
		}
		break
	}
	fragment := builder.String()
	if fragment == "" {
		return ""
	}
	if allowPrefix {
		if !strings.HasPrefix(fragment, "sk-ant-oat01-") {
			return ""
		}
		return fragment
	}
	if trimmed != fragment {
		return ""
	}
	return fragment
}

func claudeSetupCommand() *exec.Cmd {
	if runtime.GOOS == "darwin" {
		return exec.Command("script", "-q", "/dev/null", "claude", "setup-token")
	}
	return exec.Command("script", "-q", "-c", "claude setup-token", "/dev/null")
}

func redactSensitiveTerminalOutput(raw string) string {
	return tokenRedactionPattern.ReplaceAllStringFunc(raw, func(token string) string {
		token = strings.ReplaceAll(token, "\n", "")
		token = strings.ReplaceAll(token, "\r", "")
		token = strings.TrimSpace(token)
		if token == "" {
			return token
		}
		if len(token) <= 18 {
			return "[REDACTED]"
		}
		return token[:12] + "..." + token[len(token)-6:]
	})
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
