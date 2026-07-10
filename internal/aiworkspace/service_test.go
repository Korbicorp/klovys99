package aiworkspace

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServiceMetadataIncludesExpectedProviders(t *testing.T) {
	t.Setenv(aiWorkspaceKeyEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()

	providers := service.Metadata()
	if len(providers) != 4 {
		t.Fatalf("len(providers) = %d, want 4", len(providers))
	}
	if providers[0].ID != "claude" || providers[0].DefaultMethod != "api_key" {
		t.Fatalf("claude metadata = %#v, want api_key default", providers[0])
	}
	for _, method := range providers[0].Methods {
		if method.ID == "oauth_token" {
			t.Fatalf("claude metadata = %#v, want oauth_token method removed", providers[0])
		}
	}
}

func TestServiceMetadataMarksStoredAPIKeyAsAvailable(t *testing.T) {
	t.Setenv(aiWorkspaceKeyEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()
	if err := service.creds.Save("gemini", "api_key", map[string]string{"api_key": "AIza-demo"}); err != nil {
		t.Fatalf("save credentials: %v", err)
	}

	providers := service.Metadata()
	var gemini ProviderDescriptor
	for _, provider := range providers {
		if provider.ID == "gemini" {
			gemini = provider
			break
		}
	}
	if !gemini.Available {
		t.Fatalf("gemini metadata = %#v, want available with stored credentials", gemini)
	}
	if len(gemini.Methods) == 0 || !gemini.Methods[0].Available {
		t.Fatalf("gemini methods = %#v, want api_key method available", gemini.Methods)
	}
}

func TestServiceSaveCredentialsPersistsAndMarksProviderAvailable(t *testing.T) {
	t.Setenv(aiWorkspaceKeyEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()

	descriptor, err := service.SaveCredentials("openai", SaveCredentialsRequest{
		Method: "api_key",
		Config: map[string]string{"api_key": "sk-demo"},
	})
	if err != nil {
		t.Fatalf("SaveCredentials returned error: %v", err)
	}
	if !descriptor.Available {
		t.Fatalf("descriptor = %#v, want available provider", descriptor)
	}

	storedConfig, err := service.creds.Load("openai", "api_key")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if storedConfig["api_key"] != "sk-demo" {
		t.Fatalf("storedConfig = %#v, want persisted api key", storedConfig)
	}
}

func TestServiceSaveCredentialsRejectsMissingRequiredField(t *testing.T) {
	t.Setenv(aiWorkspaceKeyEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()

	_, err := service.SaveCredentials("openai", SaveCredentialsRequest{
		Method: "api_key",
		Config: map[string]string{},
	})
	if err == nil {
		t.Fatal("SaveCredentials returned nil error, want validation error")
	}
	apiErr, ok := err.(*apiError)
	if !ok {
		t.Fatalf("error type = %T, want *apiError", err)
	}
	if apiErr.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", apiErr.StatusCode)
	}
}

func TestServiceSaveCredentialsRejectsUnsupportedProvider(t *testing.T) {
	service := NewService()

	_, err := service.SaveCredentials("vertex", SaveCredentialsRequest{
		Config: map[string]string{"api_key": "sk-demo"},
	})
	if err == nil {
		t.Fatal("SaveCredentials returned nil error, want unsupported provider error")
	}
	apiErr, ok := err.(*apiError)
	if !ok {
		t.Fatalf("error type = %T, want *apiError", err)
	}
	if apiErr.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", apiErr.StatusCode)
	}
}

func TestServiceCompleteRejectsUnsupportedProviderBeforeNetwork(t *testing.T) {
	service := NewService()

	_, err := service.Complete(context.Background(), CompletionRequest{
		Provider:         "vertex",
		Method:           "service_account",
		Model:            "claude-sonnet-4-5@20250929",
		AnonymizedPrompt: "[EMAIL_1]",
		Config: map[string]string{
			"project_id":           "demo",
			"region":               "global",
			"service_account_json": "{}",
		},
	})
	if err == nil {
		t.Fatal("Complete returned nil error, want unsupported provider error")
	}
	apiErr, ok := err.(*apiError)
	if !ok {
		t.Fatalf("error type = %T, want *apiError", err)
	}
	if apiErr.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", apiErr.StatusCode)
	}
}

func TestServiceCompleteRejectsRemovedClaudeOAuthMethod(t *testing.T) {
	service := NewService()

	_, err := service.Complete(context.Background(), CompletionRequest{
		Provider:         "claude",
		Method:           "oauth_token",
		Model:            "claude-sonnet-4-6",
		AnonymizedPrompt: "[EMAIL_1]",
	})
	if err == nil {
		t.Fatal("Complete returned nil error, want unsupported provider method error")
	}
	apiErr, ok := err.(*apiError)
	if !ok {
		t.Fatalf("error type = %T, want *apiError", err)
	}
	if apiErr.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", apiErr.StatusCode)
	}
	if apiErr.Message != "Unsupported provider method" {
		t.Fatalf("message = %q, want unsupported provider method", apiErr.Message)
	}
}

func TestServiceCompleteRejectsMissingConfigField(t *testing.T) {
	service := NewService()

	_, err := service.Complete(context.Background(), CompletionRequest{
		Provider:         "openai",
		Method:           "api_key",
		Model:            "gpt-5.1-mini",
		AnonymizedPrompt: "[EMAIL_1]",
		Config:           map[string]string{},
	})
	if err == nil {
		t.Fatal("Complete returned nil error, want validation error")
	}
	apiErr, ok := err.(*apiError)
	if !ok {
		t.Fatalf("error type = %T, want *apiError", err)
	}
	if apiErr.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", apiErr.StatusCode)
	}
}

func TestServiceCompleteUsesDefaultMethodAndModel(t *testing.T) {
	t.Setenv(aiWorkspaceKeyEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()
	service.providers["openai"] = providerDefinition{
		descriptor: service.providers["openai"].descriptor,
		methods: map[string]providerMethod{
			"api_key": {
				descriptor: service.providers["openai"].methods["api_key"].descriptor,
				call: func(_ context.Context, _ *http.Client, model string, _ string, messages []ConversationMessage, config map[string]string) (string, error) {
					if model != "gpt-5.1-mini" {
						t.Fatalf("model = %q, want default model", model)
					}
					if len(messages) != 1 || messages[0].Content != "[EMAIL_1]" {
						t.Fatalf("messages = %#v, want anonymized prompt in transcript", messages)
					}
					if config["api_key"] != "sk-demo" {
						t.Fatalf("config = %#v, want api key preserved", config)
					}
					return "ok", nil
				},
			},
		},
	}

	response, err := service.Complete(context.Background(), CompletionRequest{
		Provider:         "openai",
		AnonymizedPrompt: "[EMAIL_1]",
		Config:           map[string]string{"api_key": "sk-demo"},
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if response.Method != "api_key" || response.Model != "gpt-5.1-mini" {
		t.Fatalf("response = %#v, want default method and model", response)
	}
}

func TestServiceCompleteUsesStoredAPIKeyWhenRequestConfigIsEmpty(t *testing.T) {
	t.Setenv(aiWorkspaceKeyEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()
	if err := service.creds.Save("openai", "api_key", map[string]string{"api_key": "sk-stored"}); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
	service.providers["openai"] = providerDefinition{
		descriptor: service.providers["openai"].descriptor,
		methods: map[string]providerMethod{
			"api_key": {
				descriptor: service.providers["openai"].methods["api_key"].descriptor,
				call: func(_ context.Context, _ *http.Client, _ string, _ string, _ []ConversationMessage, config map[string]string) (string, error) {
					if config["api_key"] != "sk-stored" {
						t.Fatalf("config = %#v, want stored api key", config)
					}
					return "ok", nil
				},
			},
		},
	}

	_, err := service.Complete(context.Background(), CompletionRequest{
		Provider:         "openai",
		AnonymizedPrompt: "[EMAIL_1]",
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
}

func TestServiceCompletePersistsAPIKeyAfterSuccess(t *testing.T) {
	t.Setenv(aiWorkspaceKeyEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()
	service.providers["openai"] = providerDefinition{
		descriptor: service.providers["openai"].descriptor,
		methods: map[string]providerMethod{
			"api_key": {
				descriptor: service.providers["openai"].methods["api_key"].descriptor,
				call: func(_ context.Context, _ *http.Client, _ string, _ string, _ []ConversationMessage, config map[string]string) (string, error) {
					if config["api_key"] != "sk-demo" {
						t.Fatalf("config = %#v, want request api key", config)
					}
					return "ok", nil
				},
			},
		},
	}

	_, err := service.Complete(context.Background(), CompletionRequest{
		Provider:         "openai",
		AnonymizedPrompt: "[EMAIL_1]",
		Config:           map[string]string{"api_key": "sk-demo"},
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	storedConfig, err := service.creds.Load("openai", "api_key")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if storedConfig["api_key"] != "sk-demo" {
		t.Fatalf("storedConfig = %#v, want persisted api key", storedConfig)
	}
}

func TestServiceConversationLifecyclePersistsAndReloads(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv(aiWorkspaceDirEnv, stateDir)

	service := NewService()
	conversation, err := service.CreateConversation()
	if err != nil {
		t.Fatalf("CreateConversation returned error: %v", err)
	}
	conversation.Title = "Thread"
	conversation.Provider = "openai"
	conversation.Method = "api_key"
	conversation.Model = "gpt-5.1-mini"
	conversation.Messages = []ConversationMessage{
		{Role: "user", Content: "[EMAIL_1]", CreatedAt: time.Now().UTC()},
	}
	conversation.UpdatedAt = conversation.Messages[0].CreatedAt
	if err := service.store.Save(conversation); err != nil {
		t.Fatalf("save conversation: %v", err)
	}

	reloaded := NewService()
	got, err := reloaded.GetConversation(conversation.ID)
	if err != nil {
		t.Fatalf("GetConversation returned error: %v", err)
	}
	if got.ID != conversation.ID || len(got.Messages) != 1 || got.Messages[0].Content != "[EMAIL_1]" {
		t.Fatalf("conversation = %#v, want persisted conversation", got)
	}
}

func TestServiceCompletePersistsConversationAndTruncatesProviderHistory(t *testing.T) {
	t.Setenv(aiWorkspaceKeyEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()
	now := time.Now().UTC().Add(-time.Hour)
	conversation, err := service.CreateConversation()
	if err != nil {
		t.Fatalf("CreateConversation returned error: %v", err)
	}
	for index := 0; index < 16; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		conversation.Messages = append(conversation.Messages, ConversationMessage{
			Role:      role,
			Content:   fmt.Sprintf("message-%02d", index),
			CreatedAt: now.Add(time.Duration(index) * time.Minute),
		})
	}
	conversation.Title = "Old"
	conversation.UpdatedAt = conversation.Messages[len(conversation.Messages)-1].CreatedAt
	if err := service.store.Save(conversation); err != nil {
		t.Fatalf("save conversation: %v", err)
	}

	service.providers["openai"] = providerDefinition{
		descriptor: service.providers["openai"].descriptor,
		methods: map[string]providerMethod{
			"api_key": {
				descriptor: service.providers["openai"].methods["api_key"].descriptor,
				call: func(_ context.Context, _ *http.Client, _ string, _ string, messages []ConversationMessage, _ map[string]string) (string, error) {
					if len(messages) != 15 {
						t.Fatalf("len(messages) = %d, want 15", len(messages))
					}
					if messages[0].Content != "message-02" {
						t.Fatalf("first message = %q, want truncated history", messages[0].Content)
					}
					if messages[14].Content != "[NEW_EMAIL_1]" {
						t.Fatalf("last message = %q, want latest user message", messages[14].Content)
					}
					return "assistant-response", nil
				},
			},
		},
	}

	response, err := service.Complete(context.Background(), CompletionRequest{
		ConversationID:   conversation.ID,
		Provider:         "openai",
		Method:           "api_key",
		Model:            "gpt-5.1-mini",
		AnonymizedPrompt: "[NEW_EMAIL_1]",
		Config:           map[string]string{"api_key": "sk-demo"},
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if response.ConversationID != conversation.ID {
		t.Fatalf("ConversationID = %q, want existing conversation id", response.ConversationID)
	}

	stored, err := service.GetConversation(conversation.ID)
	if err != nil {
		t.Fatalf("GetConversation returned error: %v", err)
	}
	if len(stored.Messages) != 18 {
		t.Fatalf("len(stored.Messages) = %d, want full persisted history", len(stored.Messages))
	}
	if stored.Messages[16].Content != "[NEW_EMAIL_1]" || stored.Messages[17].Content != "assistant-response" {
		t.Fatalf("stored messages tail = %#v, want persisted user+assistant messages", stored.Messages[len(stored.Messages)-2:])
	}
}

func TestServiceListConversationsSortsByUpdatedAt(t *testing.T) {
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()
	first, err := service.CreateConversation()
	if err != nil {
		t.Fatalf("CreateConversation first returned error: %v", err)
	}
	second, err := service.CreateConversation()
	if err != nil {
		t.Fatalf("CreateConversation second returned error: %v", err)
	}
	first.Title = "First"
	second.Title = "Second"
	first.UpdatedAt = time.Now().UTC().Add(-time.Hour)
	second.UpdatedAt = time.Now().UTC()
	if err := service.store.Save(first); err != nil {
		t.Fatalf("save first conversation: %v", err)
	}
	if err := service.store.Save(second); err != nil {
		t.Fatalf("save second conversation: %v", err)
	}

	conversations, err := service.ListConversations()
	if err != nil {
		t.Fatalf("ListConversations returned error: %v", err)
	}
	if len(conversations) != 2 || conversations[0].ID != second.ID || conversations[1].ID != first.ID {
		t.Fatalf("conversations = %#v, want sorted by updated_at desc", conversations)
	}
}

func TestServiceCompleteRejectsEmptyAnonymizedPrompt(t *testing.T) {
	service := NewService()

	_, err := service.Complete(context.Background(), CompletionRequest{
		Provider: "claude",
		Method:   "api_key",
		Model:    "claude-sonnet-4-6",
		Config:   map[string]string{"api_key": "sk-demo"},
	})
	if err == nil {
		t.Fatal("Complete returned nil error, want anonymized prompt error")
	}
	apiErr, ok := err.(*apiError)
	if !ok {
		t.Fatalf("error type = %T, want *apiError", err)
	}
	if apiErr.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", apiErr.StatusCode)
	}
}

func TestClaudeOAuthEncryptedStorage(t *testing.T) {
	t.Setenv(aiWorkspaceKeyEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())

	path := filepath.Join(os.Getenv(aiWorkspaceDirEnv), "claude", "oauth_token.enc")
	if err := writeEncryptedFile(path, "sk-ant-oat01-demo"); err != nil {
		t.Fatalf("writeEncryptedFile returned error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if bytes.Contains(raw, []byte("sk-ant-oat01-demo")) {
		t.Fatal("encrypted file contains plaintext token")
	}

	token, err := readEncryptedFile(path)
	if err != nil {
		t.Fatalf("readEncryptedFile returned error: %v", err)
	}
	if token != "sk-ant-oat01-demo" {
		t.Fatalf("token = %q, want saved token", token)
	}
}

func TestServiceClaudeOAuthLifecycle(t *testing.T) {
	t.Setenv(aiWorkspaceKeyEnv, "test-secret")
	stateDir := t.TempDir()
	runner := &fakeClaudeRunner{
		t:            t,
		expectedCode: "123456",
	}

	service := NewService()
	service.oauth = newClaudeOAuthManager(stateDir, runner)

	started, err := service.StartClaudeOAuth(context.Background())
	if err != nil {
		t.Fatalf("StartClaudeOAuth returned error: %v", err)
	}
	if got, want := started.AuthorizationURL, "https://claude.com/cai/oauth/authorize?demo=1&redirect_uri=https%3A%2F%2Fold.example%2Fcallback"; got != want {
		t.Fatalf("url = %q, want captured OAuth URL", got)
	}

	status, err := service.SubmitClaudeOAuth(context.Background(), ClaudeOAuthSubmitRequest{Code: "123456"})
	if err != nil {
		t.Fatalf("SubmitClaudeOAuth returned error: %v", err)
	}
	if !status.Linked || status.Pending {
		t.Fatalf("status = %#v, want linked=true and pending=false", status)
	}
}

func TestServiceCancelClaudeOAuthLinkStopsSession(t *testing.T) {
	t.Setenv(aiWorkspaceKeyEnv, "test-secret")
	done := make(chan struct{})
	runner := &fakeClaudeRunner{t: t, done: done}

	service := NewService()
	service.oauth = newClaudeOAuthManager(t.TempDir(), runner)
	if _, err := service.StartClaudeOAuth(context.Background()); err != nil {
		t.Fatalf("StartClaudeOAuth returned error: %v", err)
	}

	if err := service.CancelClaudeOAuth(); err != nil {
		t.Fatalf("CancelClaudeOAuth returned error: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not close the OAuth session")
	}
	if status := service.ClaudeOAuthStatus(); status.Pending {
		t.Fatalf("status = %#v, want no pending session", status)
	}
}

type fakeClaudeRunner struct {
	t              *testing.T
	done           chan struct{}
	response       string
	expectedCode   string
	expectedModel  string
	expectedSystem string
	expectedPrompt string
	lastToken      string
}

func (r *fakeClaudeRunner) StartSetup(configDir string) (*oauthLinkSession, error) {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, err
	}
	stdoutReader, stdoutWriter := io.Pipe()
	stdinReader, stdinWriter := io.Pipe()
	session := &oauthLinkSession{
		stdin: stdinWriter,
		done:  make(chan struct{}),
	}
	go session.capture(stdoutReader)
	go func() {
		_, _ = stdoutWriter.Write([]byte("\x1b[0mOpen https://claude.com/cai/oauth/authorize?demo=1&redirect_uri=https%3A%2F%2Fold.example%2Fcallback now\r\n"))
		buffer := make([]byte, 64)
		n, _ := stdinReader.Read(buffer)
		if r.expectedCode != "" && string(buffer[:n]) != r.expectedCode+"\n" {
			r.t.Errorf("stdin = %q, want %q", string(buffer[:n]), r.expectedCode+"\n")
		}
		if r.expectedCode != "" {
			_, _ = stdoutWriter.Write([]byte("token: sk-ant-oat01-demo_token\r\n"))
		}
		_ = stdoutWriter.Close()
		close(session.done)
		if r.done != nil {
			close(r.done)
		}
	}()
	return session, nil
}

func (r *fakeClaudeRunner) RunOAuthPrompt(_ context.Context, _ string, token string, model string, systemPrompt string, prompt string) (string, error) {
	r.lastToken = token
	if r.expectedModel != "" && model != r.expectedModel {
		r.t.Fatalf("model = %q, want %q", model, r.expectedModel)
	}
	if r.expectedSystem != "" && systemPrompt != r.expectedSystem {
		r.t.Fatalf("systemPrompt = %q, want %q", systemPrompt, r.expectedSystem)
	}
	if r.expectedPrompt != "" && prompt != r.expectedPrompt {
		r.t.Fatalf("prompt = %q, want %q", prompt, r.expectedPrompt)
	}
	return r.response, nil
}
