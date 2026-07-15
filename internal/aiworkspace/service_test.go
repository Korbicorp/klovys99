package aiworkspace

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Korbicorp/klovys99/internal/anonymizer"
	"github.com/Korbicorp/klovys99/internal/credential"
)

func TestServiceMetadataIncludesExpectedProviders(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	addFakeClaudeOAuthBinariesToPath(t)
	service := NewService()

	providers := service.Metadata()
	if len(providers) != 4 {
		t.Fatalf("len(providers) = %d, want 4", len(providers))
	}
	if providers[0].ID != "claude" || providers[0].DefaultMethod != "api_key" {
		t.Fatalf("claude metadata = %#v, want api_key default before OAuth link", providers[0])
	}
	oauthMethod := findMethodDescriptor(providers[0].Methods, "oauth_token")
	if oauthMethod == nil {
		t.Fatalf("claude metadata = %#v, want oauth_token method exposed", providers[0])
	}
	if oauthMethod.Available || oauthMethod.UnavailableReason != "Connect Claude OAuth in Compte IA" {
		t.Fatalf("claude oauth method = %#v, want unavailable oauth_token before link", oauthMethod)
	}
}

func TestServiceMetadataMarksStoredAPIKeyAsAvailable(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()
	if err := service.creds.SaveProvider("gemini", "api_key", map[string]string{"api_key": "AIza-demo"}); err != nil {
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
	t.Setenv(credential.SecretEnv, "test-secret")
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

	storedConfig, err := service.creds.LoadProvider("openai", "api_key")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if storedConfig["api_key"] != "sk-demo" {
		t.Fatalf("storedConfig = %#v, want persisted api key", storedConfig)
	}
}

func TestServiceSaveCredentialsRejectsMissingRequiredField(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
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
	t.Setenv(credential.SecretEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()
	descriptor := service.providers["openai"].descriptor
	descriptor.Available = true
	descriptor.UnavailableReason = ""
	methodDescriptor := service.providers["openai"].methods["api_key"].descriptor
	methodDescriptor.Available = true
	methodDescriptor.UnavailableReason = ""
	service.providers["openai"] = providerDefinition{
		descriptor: descriptor,
		methods: map[string]providerMethod{
			"api_key": {
				descriptor: methodDescriptor,
				call: func(_ context.Context, _ *http.Client, model string, _ string, messages []ConversationMessage, config map[string]string) (providerCallResult, error) {
					if model != "gpt-5.1-mini" {
						t.Fatalf("model = %q, want default model", model)
					}
					if len(messages) != 1 || messages[0].Content != "[EMAIL_1]" {
						t.Fatalf("messages = %#v, want anonymized prompt in transcript", messages)
					}
					if config["api_key"] != "sk-demo" {
						t.Fatalf("config = %#v, want api key preserved", config)
					}
					return providerCallResult{ResponseText: "ok"}, nil
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
	t.Setenv(credential.SecretEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()
	if err := service.creds.SaveProvider("openai", "api_key", map[string]string{"api_key": "sk-stored"}); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
	descriptor := service.providers["openai"].descriptor
	descriptor.Available = true
	descriptor.UnavailableReason = ""
	methodDescriptor := service.providers["openai"].methods["api_key"].descriptor
	methodDescriptor.Available = true
	methodDescriptor.UnavailableReason = ""
	service.providers["openai"] = providerDefinition{
		descriptor: descriptor,
		methods: map[string]providerMethod{
			"api_key": {
				descriptor: methodDescriptor,
				call: func(_ context.Context, _ *http.Client, _ string, _ string, _ []ConversationMessage, config map[string]string) (providerCallResult, error) {
					if config["api_key"] != "sk-stored" {
						t.Fatalf("config = %#v, want stored api key", config)
					}
					return providerCallResult{ResponseText: "ok"}, nil
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
	t.Setenv(credential.SecretEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()
	descriptor := service.providers["openai"].descriptor
	descriptor.Available = true
	descriptor.UnavailableReason = ""
	methodDescriptor := service.providers["openai"].methods["api_key"].descriptor
	methodDescriptor.Available = true
	methodDescriptor.UnavailableReason = ""
	service.providers["openai"] = providerDefinition{
		descriptor: descriptor,
		methods: map[string]providerMethod{
			"api_key": {
				descriptor: methodDescriptor,
				call: func(_ context.Context, _ *http.Client, _ string, _ string, _ []ConversationMessage, config map[string]string) (providerCallResult, error) {
					if config["api_key"] != "sk-demo" {
						t.Fatalf("config = %#v, want request api key", config)
					}
					return providerCallResult{ResponseText: "ok"}, nil
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
	storedConfig, err := service.creds.LoadProvider("openai", "api_key")
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
	t.Setenv(credential.SecretEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()
	descriptor := service.providers["openai"].descriptor
	descriptor.Available = true
	descriptor.UnavailableReason = ""
	methodDescriptor := service.providers["openai"].methods["api_key"].descriptor
	methodDescriptor.Available = true
	methodDescriptor.UnavailableReason = ""
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
		descriptor: descriptor,
		methods: map[string]providerMethod{
			"api_key": {
				descriptor: methodDescriptor,
				call: func(_ context.Context, _ *http.Client, _ string, _ string, messages []ConversationMessage, _ map[string]string) (providerCallResult, error) {
					if len(messages) != 15 {
						t.Fatalf("len(messages) = %d, want 15", len(messages))
					}
					if messages[0].Content != "message-02" {
						t.Fatalf("first message = %q, want truncated history", messages[0].Content)
					}
					if messages[14].Content != "[NEW_EMAIL_1]" {
						t.Fatalf("last message = %q, want latest user message", messages[14].Content)
					}
					return providerCallResult{ResponseText: "assistant-response"}, nil
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

func TestServiceCompleteRestoresProviderResponseButPersistsAnonymizedConversation(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())

	shared := newSharedEmailAnonymizer(t)
	seedKnownEmailToken(t, shared, "alice@example.com")

	service := NewService(shared)
	descriptor := service.providers["openai"].descriptor
	descriptor.Available = true
	descriptor.UnavailableReason = ""
	methodDescriptor := service.providers["openai"].methods["api_key"].descriptor
	methodDescriptor.Available = true
	methodDescriptor.UnavailableReason = ""
	service.providers["openai"] = providerDefinition{
		descriptor: descriptor,
		methods: map[string]providerMethod{
			"api_key": {
				descriptor: methodDescriptor,
				call: func(_ context.Context, _ *http.Client, _ string, _ string, messages []ConversationMessage, _ map[string]string) (providerCallResult, error) {
					if len(messages) != 1 || messages[0].Content != "[EMAIL_1]" {
						t.Fatalf("messages = %#v, want anonymized provider transcript", messages)
					}
					return providerCallResult{ResponseText: "Reply to [EMAIL_1]"}, nil
				},
			},
		},
	}

	response, err := service.Complete(context.Background(), CompletionRequest{
		Provider:         "openai",
		Method:           "api_key",
		Model:            "gpt-5.1-mini",
		AnonymizedPrompt: "[EMAIL_1]",
		Config:           map[string]string{"api_key": "sk-demo"},
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if got, want := response.ResponseText, "Reply to alice@example.com"; got != want {
		t.Fatalf("ResponseText = %q, want %q", got, want)
	}

	stored, err := service.store.Get(response.ConversationID)
	if err != nil {
		t.Fatalf("store.Get returned error: %v", err)
	}
	if len(stored.Messages) != 2 {
		t.Fatalf("len(stored.Messages) = %d, want 2", len(stored.Messages))
	}
	if stored.Messages[0].Content != "[EMAIL_1]" || stored.Messages[1].Content != "Reply to [EMAIL_1]" {
		t.Fatalf("stored messages = %#v, want anonymized persisted content", stored.Messages)
	}

	restored, err := service.GetConversation(response.ConversationID)
	if err != nil {
		t.Fatalf("GetConversation returned error: %v", err)
	}
	if restored.Messages[0].Content != "alice@example.com" || restored.Messages[1].Content != "Reply to alice@example.com" {
		t.Fatalf("restored messages = %#v, want restored display content", restored.Messages)
	}
}

func TestServiceGetConversationAndListRestorePersistedContentAcrossRestart(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	stateDir := t.TempDir()
	t.Setenv(aiWorkspaceDirEnv, stateDir)

	storePath := filepath.Join(stateDir, "tokens.sqlite")
	tokenStore, err := anonymizer.NewSQLiteTokenStore(storePath)
	if err != nil {
		t.Fatalf("NewSQLiteTokenStore() error = %v", err)
	}
	shared := anonymizer.NewServiceWithTokenStore([]anonymizer.Detector{
		exactMatchDetector{target: "alice@example.com", entityType: anonymizer.EntityEmail, normalized: "alice@example.com"},
	}, tokenStore)
	seedKnownEmailToken(t, shared, "alice@example.com")

	service := NewService(shared)
	conversation, err := service.CreateConversation()
	if err != nil {
		t.Fatalf("CreateConversation returned error: %v", err)
	}
	conversation.Title = "Thread [EMAIL_1]"
	conversation.Messages = []ConversationMessage{
		{Role: "user", Content: "[EMAIL_1]", CreatedAt: time.Now().UTC()},
		{Role: "assistant", Content: "Reply to [EMAIL_1]", CreatedAt: time.Now().UTC()},
	}
	conversation.UpdatedAt = conversation.Messages[len(conversation.Messages)-1].CreatedAt
	if err := service.store.Save(conversation); err != nil {
		t.Fatalf("save conversation: %v", err)
	}
	if err := tokenStore.Close(); err != nil {
		t.Fatalf("tokenStore.Close() error = %v", err)
	}

	reopenedStore, err := anonymizer.NewSQLiteTokenStore(storePath)
	if err != nil {
		t.Fatalf("reopen NewSQLiteTokenStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = reopenedStore.Close()
	})
	reloaded := NewService(anonymizer.NewServiceWithTokenStore(nil, reopenedStore))

	got, err := reloaded.GetConversation(conversation.ID)
	if err != nil {
		t.Fatalf("GetConversation returned error: %v", err)
	}
	if got.Title != "Thread alice@example.com" {
		t.Fatalf("Title = %q, want restored title", got.Title)
	}
	if got.Messages[0].Content != "alice@example.com" || got.Messages[1].Content != "Reply to alice@example.com" {
		t.Fatalf("messages = %#v, want restored messages after restart", got.Messages)
	}

	summaries, err := reloaded.ListConversations()
	if err != nil {
		t.Fatalf("ListConversations returned error: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Title != "Thread alice@example.com" {
		t.Fatalf("summaries = %#v, want restored summary title", summaries)
	}
}

func TestServiceLeavesUnknownPlaceholderUnchangedWhenNoMappingExists(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())

	service := NewService(newSharedEmailAnonymizer(t))
	conversation, err := service.CreateConversation()
	if err != nil {
		t.Fatalf("CreateConversation returned error: %v", err)
	}
	conversation.Title = "Thread [EMAIL_999]"
	conversation.Messages = []ConversationMessage{
		{Role: "assistant", Content: "Unknown [EMAIL_999]", CreatedAt: time.Now().UTC()},
	}
	conversation.UpdatedAt = conversation.Messages[0].CreatedAt
	if err := service.store.Save(conversation); err != nil {
		t.Fatalf("save conversation: %v", err)
	}

	got, err := service.GetConversation(conversation.ID)
	if err != nil {
		t.Fatalf("GetConversation returned error: %v", err)
	}
	if got.Title != "Thread [EMAIL_999]" || got.Messages[0].Content != "Unknown [EMAIL_999]" {
		t.Fatalf("conversation = %#v, want unknown placeholders unchanged", got)
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
	t.Setenv(credential.SecretEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())

	store := credential.NewStore(os.Getenv(aiWorkspaceDirEnv))
	path := store.OAuthTokenPath("claude", "oauth_token")
	if err := store.SaveOAuthToken("claude", "oauth_token", "sk-ant-oat01-demo"); err != nil {
		t.Fatalf("SaveOAuthToken returned error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if bytes.Contains(raw, []byte("sk-ant-oat01-demo")) {
		t.Fatal("encrypted file contains plaintext token")
	}

	token, err := store.LoadOAuthToken("claude", "oauth_token")
	if err != nil {
		t.Fatalf("LoadOAuthToken returned error: %v", err)
	}
	if token != "sk-ant-oat01-demo" {
		t.Fatalf("token = %q, want saved token", token)
	}
}

func TestClaudeOAuthStatusReportsLinkedMethodWhenTokenExists(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	stateDir := t.TempDir()
	manager := newClaudeOAuthManager(stateDir, credential.NewStore(stateDir), &fakeClaudeRunner{t: t})

	if err := manager.creds.SaveOAuthToken("claude", "oauth_token", "sk-ant-oat01-demo_token"); err != nil {
		t.Fatalf("SaveOAuthToken returned error: %v", err)
	}

	status := manager.status()
	if !status.Linked {
		t.Fatalf("status = %#v, want linked token", status)
	}
	if status.Pending {
		t.Fatalf("status = %#v, want no pending session", status)
	}
	if status.Method != "oauth_token" {
		t.Fatalf("status.Method = %q, want oauth_token", status.Method)
	}
}

func TestServiceMetadataMarksClaudeOAuthAsAvailableWhenTokenExists(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	stateDir := t.TempDir()
	t.Setenv(aiWorkspaceDirEnv, stateDir)
	addFakeClaudeOAuthBinariesToPath(t)
	service := NewService()

	if err := service.creds.SaveOAuthToken("claude", "oauth_token", "sk-ant-oat01-demo"); err != nil {
		t.Fatalf("SaveOAuthToken returned error: %v", err)
	}

	providers := service.Metadata()
	claude := providers[0]
	oauthMethod := findMethodDescriptor(claude.Methods, "oauth_token")
	if oauthMethod == nil || !oauthMethod.Available {
		t.Fatalf("claude methods = %#v, want available oauth_token", claude.Methods)
	}
	if claude.DefaultMethod != "oauth_token" {
		t.Fatalf("claude default method = %q, want oauth_token", claude.DefaultMethod)
	}
}

func TestServiceMetadataMarksClaudeOAuthUnavailableWhenCLIIsMissing(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	stateDir := t.TempDir()
	t.Setenv(aiWorkspaceDirEnv, stateDir)
	t.Setenv("PATH", t.TempDir())
	service := NewService()

	if err := service.creds.SaveOAuthToken("claude", "oauth_token", "sk-ant-oat01-demo"); err != nil {
		t.Fatalf("SaveOAuthToken returned error: %v", err)
	}

	providers := service.Metadata()
	claude := providers[0]
	oauthMethod := findMethodDescriptor(claude.Methods, "oauth_token")
	if oauthMethod == nil {
		t.Fatalf("claude methods = %#v, want oauth_token method", claude.Methods)
	}
	if oauthMethod.Available {
		t.Fatalf("claude oauth method = %#v, want unavailable when claude CLI is missing", oauthMethod)
	}
	if !strings.Contains(oauthMethod.UnavailableReason, "Claude CLI is not installed") {
		t.Fatalf("oauth unavailable reason = %q, want Claude CLI missing", oauthMethod.UnavailableReason)
	}
	if claude.DefaultMethod != "api_key" {
		t.Fatalf("claude default method = %q, want api_key when Claude CLI is missing", claude.DefaultMethod)
	}
}

func TestServiceClaudeOAuthLifecycle(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	stateDir := t.TempDir()
	addFakeClaudeOAuthBinariesToPath(t)
	runner := &fakeClaudeRunner{
		t:            t,
		expectedCode: "sSrY6NtKGr2KHfShKo1D54wC92Ea5K6F00xTsTNMBGCpQR5s#1ao66jfCtXGTby9z_AqZdcZWbKRh_0JsdND1nMWkP00",
	}

	service := NewService()
	service.oauth = newClaudeOAuthManager(stateDir, credential.NewStore(stateDir), runner)

	started, err := service.StartClaudeOAuth(context.Background())
	if err != nil {
		t.Fatalf("StartClaudeOAuth returned error: %v", err)
	}
	if started.Method != "oauth_token" || !started.Pending || started.Linked {
		t.Fatalf("started = %#v, want oauth pending response", started)
	}
	if !started.RequiresCode {
		t.Fatalf("started = %#v, want fallback requires_code=true", started)
	}
	if got, want := started.AuthorizationURL, "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&code_challenge=L431fXR10lv1xLV1xj8mufi7Xy2yRFDnBSLTCJQQAjU&code_challenge_method=S256&state=1ao66jfCtXGTby9z_AqZdcZWbKRh_0JsdND1nMWkP00"; got != want {
		t.Fatalf("url = %q, want captured OAuth URL", got)
	}

	status, err := service.SubmitClaudeOAuth(context.Background(), ClaudeOAuthSubmitRequest{Code: "sSrY6NtKGr2KHfShKo1D54wC92Ea5K6F00xTsTNMBGCpQR5s#1ao66jfCtXGTby9z_AqZdcZWbKRh_0JsdND1nMWkP00"})
	if err != nil {
		t.Fatalf("SubmitClaudeOAuth returned error: %v", err)
	}
	if !status.Linked || status.Pending {
		t.Fatalf("status = %#v, want linked=true and pending=false", status)
	}
	if status.Method != "oauth_token" {
		t.Fatalf("status.Method = %q, want oauth_token", status.Method)
	}

	token, err := service.oauth.creds.LoadOAuthToken("claude", "oauth_token")
	if err != nil {
		t.Fatalf("LoadOAuthToken returned error: %v", err)
	}
	if token != "sk-ant-oat01-demo_token" {
		t.Fatalf("token = %q, want saved OAuth token", token)
	}
}

func TestServiceClaudeOAuthAutoConnectsWhenCLIPrintsToken(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	stateDir := t.TempDir()
	addFakeClaudeOAuthBinariesToPath(t)

	service := NewService()
	service.oauth = newClaudeOAuthManager(stateDir, credential.NewStore(stateDir), &fakeClaudeRunner{
		t:                  t,
		autoTokenFragments: []string{"sk-ant-oat01-whjJf2RQPKaV6tCYX-MO3v-", "ZvS8kZwhK9gj6eyxnftZ3vW3txZdb8paFKHfR5GvmfZ9LPc7tQdfLnTPk47CkGA-wKkfiAAA"},
	})

	started, err := service.StartClaudeOAuth(context.Background())
	if err != nil {
		t.Fatalf("StartClaudeOAuth returned error: %v", err)
	}
	if started.RequiresCode {
		t.Fatalf("started = %#v, want requires_code=false for auto token flow", started)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := service.ClaudeOAuthStatus()
		if status.Linked {
			if status.Pending {
				t.Fatalf("status = %#v, want linked session finalized", status)
			}
			token, err := service.oauth.creds.LoadOAuthToken("claude", "oauth_token")
			if err != nil {
				t.Fatalf("LoadOAuthToken returned error: %v", err)
			}
			want := "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"
			if token != want {
				t.Fatalf("token = %q, want merged multiline OAuth token", token)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Claude OAuth status did not auto-link after token detection")
}

func TestServiceCancelClaudeOAuthLinkStopsSession(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	addFakeClaudeOAuthBinariesToPath(t)
	done := make(chan struct{})
	runner := &fakeClaudeRunner{t: t, done: done}

	service := NewService()
	stateDir := t.TempDir()
	service.oauth = newClaudeOAuthManager(stateDir, credential.NewStore(stateDir), runner)
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

func TestServiceCompleteUsesClaudeOAuthToken(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	stateDir := t.TempDir()
	t.Setenv(aiWorkspaceDirEnv, stateDir)
	runner := &fakeClaudeRunner{
		t:                 t,
		response:          "oauth-response",
		responseSessionID: "claude-session-123",
		expectedModel:     "claude-sonnet-4-6",
		expectedSystem:    defaultSystemPrompt,
		expectedPrompt:    "[EMAIL_1]",
	}

	service := NewService()
	service.oauth = newClaudeOAuthManager(stateDir, credential.NewStore(stateDir), runner)
	if err := service.creds.SaveOAuthToken("claude", "oauth_token", "sk-ant-oat01-demo_token"); err != nil {
		t.Fatalf("SaveOAuthToken returned error: %v", err)
	}

	response, err := service.Complete(context.Background(), CompletionRequest{
		Provider:         "claude",
		Method:           "oauth_token",
		Model:            "claude-sonnet-4-6",
		AnonymizedPrompt: "[EMAIL_1]",
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if response.ResponseText != "oauth-response" {
		t.Fatalf("response = %#v, want oauth response", response)
	}
	if runner.lastToken != "sk-ant-oat01-demo_token" {
		t.Fatalf("runner token = %q, want stored oauth token", runner.lastToken)
	}
	conversation, err := service.GetConversation(response.ConversationID)
	if err != nil {
		t.Fatalf("GetConversation returned error: %v", err)
	}
	if conversation.ProviderSessionID != "claude-session-123" {
		t.Fatalf("conversation.ProviderSessionID = %q, want Claude session id", conversation.ProviderSessionID)
	}
}

func TestServiceCompleteClaudeOAuthUsesLatestPromptAndLocalSessionID(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	stateDir := t.TempDir()
	t.Setenv(aiWorkspaceDirEnv, stateDir)
	service := NewService()
	conversation, err := service.CreateConversation()
	if err != nil {
		t.Fatalf("CreateConversation returned error: %v", err)
	}
	runner := &fakeClaudeRunner{
		t:                 t,
		expectedSessionID: conversation.ID,
		expectedResume:    false,
		expectedPrompt:    "Et maintenant fais le resume",
		response:          "oauth-response",
	}
	service.oauth = newClaudeOAuthManager(stateDir, credential.NewStore(stateDir), runner)
	if err := service.creds.SaveOAuthToken("claude", "oauth_token", "sk-ant-oat01-demo_token"); err != nil {
		t.Fatalf("SaveOAuthToken returned error: %v", err)
	}
	conversation.Provider = "claude"
	conversation.Method = "oauth_token"
	conversation.Model = "claude-sonnet-4-6"
	conversation.Messages = []ConversationMessage{
		{Role: "user", Content: "Bonjour"},
		{Role: "assistant", Content: "Salut"},
		{Role: "user", Content: "Relance sur le point precedent"},
		{Role: "assistant", Content: "Voici le point precedent"},
	}
	if err := service.store.Save(conversation); err != nil {
		t.Fatalf("save conversation: %v", err)
	}

	_, err = service.Complete(context.Background(), CompletionRequest{
		ConversationID:   conversation.ID,
		Provider:         "claude",
		Method:           "oauth_token",
		Model:            "claude-sonnet-4-6",
		AnonymizedPrompt: "Et maintenant fais le resume",
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
}

func TestServiceCompleteClaudeOAuthResumesExistingProviderSession(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	stateDir := t.TempDir()
	t.Setenv(aiWorkspaceDirEnv, stateDir)
	service := NewService()
	runner := &fakeClaudeRunner{
		t:                 t,
		expectedSessionID: "claude-session-123",
		expectedResume:    true,
		expectedPrompt:    "Deuxieme prompt",
		response:          "oauth-response",
	}
	service.oauth = newClaudeOAuthManager(stateDir, credential.NewStore(stateDir), runner)
	if err := service.creds.SaveOAuthToken("claude", "oauth_token", "sk-ant-oat01-demo_token"); err != nil {
		t.Fatalf("SaveOAuthToken returned error: %v", err)
	}

	conversation, err := service.CreateConversation()
	if err != nil {
		t.Fatalf("CreateConversation returned error: %v", err)
	}
	conversation.Provider = "claude"
	conversation.Method = "oauth_token"
	conversation.Model = "claude-sonnet-4-6"
	conversation.ProviderSessionID = "claude-session-123"
	conversation.Messages = []ConversationMessage{
		{Role: "user", Content: "Premier prompt"},
		{Role: "assistant", Content: "Premiere reponse"},
	}
	if err := service.store.Save(conversation); err != nil {
		t.Fatalf("save conversation: %v", err)
	}

	updated, err := service.Complete(context.Background(), CompletionRequest{
		ConversationID:   conversation.ID,
		Provider:         "claude",
		Method:           "oauth_token",
		Model:            "claude-sonnet-4-6",
		AnonymizedPrompt: "Deuxieme prompt",
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if updated.ResponseText != "oauth-response" {
		t.Fatalf("response = %#v, want oauth response", updated)
	}
}

func TestClaudeConversationSessionIDUsesConversationUUIDWhenAvailable(t *testing.T) {
	const conversationID = "df9030b8-2e9d-4da7-b25d-12fd52a4977d"
	if got := claudeConversationSessionID(conversationID); got != conversationID {
		t.Fatalf("claudeConversationSessionID() = %q, want %q", got, conversationID)
	}
}

func TestClaudeConversationSessionIDDerivesDeterministicUUIDForLegacyIDs(t *testing.T) {
	const conversationID = "legacy-conversation-id"
	const want = "40c4faec-49e3-5277-9275-9afb5e4f7266"
	if got := claudeConversationSessionID(conversationID); got != want {
		t.Fatalf("claudeConversationSessionID() = %q, want %q", got, want)
	}
}

func TestServiceCompleteClearsProviderSessionIDWhenProviderDoesNotReturnOne(t *testing.T) {
	t.Setenv(credential.SecretEnv, "test-secret")
	t.Setenv(aiWorkspaceDirEnv, t.TempDir())
	service := NewService()

	conversation, err := service.CreateConversation()
	if err != nil {
		t.Fatalf("CreateConversation returned error: %v", err)
	}
	conversation.Provider = "claude"
	conversation.Method = "oauth_token"
	conversation.Model = "claude-sonnet-4-6"
	conversation.ProviderSessionID = "claude-session-123"
	if err := service.store.Save(conversation); err != nil {
		t.Fatalf("save conversation: %v", err)
	}

	descriptor := service.providers["openai"].descriptor
	descriptor.Available = true
	descriptor.UnavailableReason = ""
	methodDescriptor := service.providers["openai"].methods["api_key"].descriptor
	methodDescriptor.Available = true
	methodDescriptor.UnavailableReason = ""
	service.providers["openai"] = providerDefinition{
		descriptor: descriptor,
		methods: map[string]providerMethod{
			"api_key": {
				descriptor: methodDescriptor,
				call: func(_ context.Context, _ *http.Client, _ string, _ string, _ []ConversationMessage, _ map[string]string) (providerCallResult, error) {
					return providerCallResult{ResponseText: "ok"}, nil
				},
			},
		},
	}

	_, err = service.Complete(context.Background(), CompletionRequest{
		ConversationID:   conversation.ID,
		Provider:         "openai",
		Method:           "api_key",
		Model:            "gpt-5.1-mini",
		AnonymizedPrompt: "[EMAIL_1]",
		Config:           map[string]string{"api_key": "sk-demo"},
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}

	stored, err := service.GetConversation(conversation.ID)
	if err != nil {
		t.Fatalf("GetConversation returned error: %v", err)
	}
	if stored.ProviderSessionID != "" {
		t.Fatalf("stored.ProviderSessionID = %q, want cleared provider session id", stored.ProviderSessionID)
	}
}

func TestClaudeSetupCommandUsesPlatformCompatibleScriptSyntax(t *testing.T) {
	cmd := claudeSetupCommand()
	if runtime.GOOS == "darwin" {
		want := []string{"script", "-q", "/dev/null", "claude", "setup-token"}
		if got := cmd.Args; !equalStringSlices(got, want) {
			t.Fatalf("cmd.Args = %#v, want %#v", got, want)
		}
		return
	}
	want := []string{"script", "-q", "-c", "claude setup-token", "/dev/null"}
	if got := cmd.Args; !equalStringSlices(got, want) {
		t.Fatalf("cmd.Args = %#v, want %#v", got, want)
	}
}

func TestExtractAuthorizationURLReassemblesWrappedClaudeLink(t *testing.T) {
	raw := "Browser didn't open? Use the url below to sign in (c to copy)\r\r\n\r\r\nhttps://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88\r\r\ned-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.co\r\r\nm%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&code_challenge=L431fXR10lv1xL\r\r\nV1xj8mufi7Xy2yRFDnBSLTCJQQAjU&code_challenge_method=S256&state=1ao66jfCtXGTby9z_\r\r\nAqZdcZWbKRh_0JsdND1nMWkP00\r\r\n\r\r\n\r\r\nPastecodehereifprompted>"
	got := extractAuthorizationURL(raw)
	want := "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&code_challenge=L431fXR10lv1xLV1xj8mufi7Xy2yRFDnBSLTCJQQAjU&code_challenge_method=S256&state=1ao66jfCtXGTby9z_AqZdcZWbKRh_0JsdND1nMWkP00"
	if got != want {
		t.Fatalf("extractAuthorizationURL() = %q, want %q", got, want)
	}
}

func TestExtractOAuthTokenStopsBeforeFollowingClaudeInstructions(t *testing.T) {
	raw := "78WelcometoClaudeCodev2.1.206\n\n[>0q·Openingbrowsertosignin…\n✢\nBrowser didn't open? Use the url below to sign in (c to copy)\n\nhttps://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88\ned-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.co\nm%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&code_challenge=Tbrhn3VHs88qqi\n83h3QK3EhwL4sZYZcv3wBVlji9JV8&code_challenge_method=S256&state=WrbAQHvlohkZ2Iebe\nBE_4e3YisscrirOXitzeRxKbyY\n\n\nPastecodehereifprompted>\n     ************************************************\n**************************************RxKbyY\n✓ Long-lived authentication token created successfully!Your OAuth token (valid for 1 year):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 this token securely. You won't be able to see it again.Use this token by setting: export CLAUDE_CODE_OAUTH_TOKEN=<token>\n\n"
	got := extractOAuthToken(raw)
	want := "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"
	if got != want {
		t.Fatalf("extractOAuthToken() = %q, want %q", got, want)
	}
}

func addFakeClaudeOAuthBinariesToPath(t *testing.T) {
	t.Helper()

	binDir := t.TempDir()
	for _, name := range []string{"claude", "script"} {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatalf("write fake %s binary: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type fakeClaudeRunner struct {
	t                  *testing.T
	done               chan struct{}
	response           string
	responseSessionID  string
	expectedSessionID  string
	expectedResume     bool
	expectedCode       string
	expectedModel      string
	expectedSystem     string
	expectedPrompt     string
	autoTokenFragments []string
	lastToken          string
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
	go session.capture("stdout", stdoutReader)
	go func() {
		if len(r.autoTokenFragments) > 0 {
			_, _ = stdoutWriter.Write([]byte("\x1b[0mOpen https://claude.com/cai/oauth/authorize?demo=1&redirect_uri=https%3A%2F%2Fold.example%2Fcallback now\r\n"))
			_, _ = stdoutWriter.Write([]byte("\r\n ✓ Long-lived authentication token created successfully!\r\n\r\n"))
			for _, fragment := range r.autoTokenFragments {
				_, _ = stdoutWriter.Write([]byte(" " + fragment + "\r\n"))
			}
			_ = stdoutWriter.Close()
			close(session.done)
			if r.done != nil {
				close(r.done)
			}
			return
		}
		_, _ = stdoutWriter.Write([]byte("Browser didn't open? Use the url below to sign in (c to copy)\r\r\n\r\r\nhttps://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88\r\r\ned-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.co\r\r\nm%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&code_challenge=L431fXR10lv1xL\r\r\nV1xj8mufi7Xy2yRFDnBSLTCJQQAjU&code_challenge_method=S256&state=1ao66jfCtXGTby9z_\r\r\nAqZdcZWbKRh_0JsdND1nMWkP00\r\r\n\r\r\n\r\r\nPastecodehereifprompted>\r\n"))
		if r.expectedCode != "" {
			buffer := make([]byte, 256)
			n, _ := stdinReader.Read(buffer)
			if got := string(buffer[:n]); got != r.expectedCode {
				r.t.Errorf("stdin token = %q, want %q", got, r.expectedCode)
			}
			n, _ = stdinReader.Read(buffer)
			if got := string(buffer[:n]); got != "\n" && got != "\r\n" {
				r.t.Errorf("stdin enter = %q, want newline", got)
			}
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

func (r *fakeClaudeRunner) RunOAuthPrompt(_ context.Context, _ string, token string, sessionID string, resume bool, model string, systemPrompt string, prompt string) (claudeOAuthPromptResult, error) {
	r.lastToken = token
	if r.expectedSessionID != "" && sessionID != r.expectedSessionID {
		r.t.Fatalf("sessionID = %q, want %q", sessionID, r.expectedSessionID)
	}
	if resume != r.expectedResume {
		r.t.Fatalf("resume = %t, want %t", resume, r.expectedResume)
	}
	if r.expectedModel != "" && model != r.expectedModel {
		r.t.Fatalf("model = %q, want %q", model, r.expectedModel)
	}
	if r.expectedSystem != "" && systemPrompt != r.expectedSystem {
		r.t.Fatalf("systemPrompt = %q, want %q", systemPrompt, r.expectedSystem)
	}
	if r.expectedPrompt != "" && prompt != r.expectedPrompt {
		r.t.Fatalf("prompt = %q, want %q", prompt, r.expectedPrompt)
	}
	return claudeOAuthPromptResult{
		Text:      r.response,
		SessionID: r.responseSessionID,
	}, nil
}

func equalStringSlices(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type exactMatchDetector struct {
	target     string
	entityType anonymizer.EntityType
	normalized string
}

func (d exactMatchDetector) FindAll(text string) []anonymizer.Match {
	index := strings.Index(text, d.target)
	if index < 0 {
		return nil
	}
	return []anonymizer.Match{{
		Start:      index,
		End:        index + len(d.target),
		Type:       d.entityType,
		Priority:   10,
		Normalized: d.normalized,
	}}
}

func newSharedEmailAnonymizer(t *testing.T) *anonymizer.Service {
	t.Helper()

	store, err := anonymizer.NewSQLiteTokenStore(filepath.Join(t.TempDir(), "tokens.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteTokenStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return anonymizer.NewServiceWithTokenStore([]anonymizer.Detector{
		exactMatchDetector{target: "alice@example.com", entityType: anonymizer.EntityEmail, normalized: "alice@example.com"},
	}, store)
}

func seedKnownEmailToken(t *testing.T, engine *anonymizer.Service, value string) {
	t.Helper()

	run := engine.NewRun()
	defer func() {
		_ = run.Close()
	}()
	output, _ := run.Anonymize(value)
	if got, want := output, "[EMAIL_1]"; got != want {
		t.Fatalf("Anonymize(%q) = %q, want %q", value, got, want)
	}
}
