package aiworkspace

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const maxProviderConversationMessages = 15

type ConversationSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Provider  string    `json:"provider"`
	Method    string    `json:"method"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ConversationMessage struct {
	Role            string           `json:"role"`
	Content         string           `json:"content"`
	CreatedAt       time.Time        `json:"created_at"`
	PIIReplacements []PIIReplacement `json:"pii_replacements,omitempty"`
}

type PIIReplacement struct {
	Type  string `json:"type"`
	Token string `json:"token"`
	Value string `json:"value"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

type ConversationDetail struct {
	ID                string                `json:"id"`
	Title             string                `json:"title"`
	Provider          string                `json:"provider"`
	Method            string                `json:"method"`
	Model             string                `json:"model"`
	ProviderSessionID string                `json:"provider_session_id,omitempty"`
	CreatedAt         time.Time             `json:"created_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
	Messages          []ConversationMessage `json:"messages"`
}

type conversationStore struct {
	mu   sync.Mutex
	path string
}

type conversationStorePayload struct {
	Conversations []ConversationDetail `json:"conversations"`
}

func newConversationStore(stateDir string) *conversationStore {
	return &conversationStore{
		path: filepath.Join(stateDir, "conversations.json"),
	}
}

func (s *conversationStore) List() ([]ConversationSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	conversations, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	summaries := make([]ConversationSummary, 0, len(conversations))
	for _, conversation := range conversations {
		summaries = append(summaries, summarizeConversation(conversation))
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	return summaries, nil
}

func (s *conversationStore) Create() (ConversationDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	conversations, err := s.loadLocked()
	if err != nil {
		return ConversationDetail{}, err
	}
	now := time.Now().UTC()
	conversation := ConversationDetail{
		ID:        newConversationID(),
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  []ConversationMessage{},
	}
	conversations = append(conversations, conversation)
	if err := s.saveLocked(conversations); err != nil {
		return ConversationDetail{}, err
	}
	return conversation, nil
}

func (s *conversationStore) Get(id string) (ConversationDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	conversations, err := s.loadLocked()
	if err != nil {
		return ConversationDetail{}, err
	}
	for _, conversation := range conversations {
		if conversation.ID == id {
			return conversation, nil
		}
	}
	return ConversationDetail{}, os.ErrNotExist
}

func (s *conversationStore) Save(conversation ConversationDetail) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	conversations, err := s.loadLocked()
	if err != nil {
		return err
	}
	replaced := false
	for index := range conversations {
		if conversations[index].ID == conversation.ID {
			conversations[index] = conversation
			replaced = true
			break
		}
	}
	if !replaced {
		conversations = append(conversations, conversation)
	}
	return s.saveLocked(conversations)
}

func (s *conversationStore) loadLocked() ([]ConversationDetail, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ConversationDetail{}, nil
		}
		return nil, err
	}
	var payload conversationStorePayload
	if len(raw) == 0 {
		return []ConversationDetail{}, nil
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload.Conversations == nil {
		return []ConversationDetail{}, nil
	}
	return payload.Conversations, nil
}

func (s *conversationStore) saveLocked(conversations []ConversationDetail) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	payload := conversationStorePayload{Conversations: conversations}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o600)
}

func summarizeConversation(conversation ConversationDetail) ConversationSummary {
	return ConversationSummary{
		ID:        conversation.ID,
		Title:     conversation.Title,
		Provider:  conversation.Provider,
		Method:    conversation.Method,
		Model:     conversation.Model,
		CreatedAt: conversation.CreatedAt,
		UpdatedAt: conversation.UpdatedAt,
	}
}

func newConversationID() string {
	return uuid.NewString()
}

func claudeConversationSessionID(conversationID string) string {
	trimmed := strings.TrimSpace(conversationID)
	if parsed, err := uuid.Parse(trimmed); err == nil {
		return parsed.String()
	}
	if trimmed == "" {
		return uuid.NewString()
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("klovys99/claude/"+trimmed)).String()
}

func legacyConversationID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339Nano), ":", "")
	}
	return hex.EncodeToString(buffer)
}

func truncateConversationTitle(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= 48 {
		return trimmed
	}
	return string(runes[:48]) + "..."
}

func providerWindow(messages []ConversationMessage) []ConversationMessage {
	if len(messages) <= maxProviderConversationMessages {
		return messages
	}
	return messages[len(messages)-maxProviderConversationMessages:]
}
