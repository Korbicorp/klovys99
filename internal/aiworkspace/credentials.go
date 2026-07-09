package aiworkspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type credentialStore struct {
	mu       sync.Mutex
	stateDir string
}

func newCredentialStore(stateDir string) *credentialStore {
	return &credentialStore{stateDir: stateDir}
}

func (s *credentialStore) Load(providerID string, methodID string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(encryptionSecret()) == "" {
		return map[string]string{}, nil
	}
	path := s.path(providerID, methodID)
	payload, err := readEncryptedFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	if strings.TrimSpace(payload) == "" {
		return map[string]string{}, nil
	}
	var config map[string]string
	if err := json.Unmarshal([]byte(payload), &config); err != nil {
		return nil, err
	}
	if config == nil {
		return map[string]string{}, nil
	}
	return config, nil
}

func (s *credentialStore) Save(providerID string, methodID string, config map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(encryptionSecret()) == "" {
		return nil
	}
	body, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return writeEncryptedFile(s.path(providerID, methodID), string(body))
}

func (s *credentialStore) Exists(providerID string, methodID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return tokenFileExists(s.path(providerID, methodID))
}

func (s *credentialStore) path(providerID string, methodID string) string {
	return filepath.Join(s.stateDir, providerID, fmt.Sprintf("%s_config.enc", methodID))
}
