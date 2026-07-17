package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const SecretEnv = "KLOVYS99_AI_WORKSPACE_KEY"

type Store struct {
	mu       sync.Mutex
	stateDir string
}

func NewStore(stateDir string) *Store {
	return &Store{stateDir: stateDir}
}

func Secret() string {
	return strings.TrimSpace(os.Getenv(SecretEnv))
}

func HasSecret() bool {
	return Secret() != ""
}

func (s *Store) SaveProvider(providerID string, methodID string, config map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !HasSecret() {
		return nil
	}
	body, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return writeEncryptedFile(s.ProviderPath(providerID, methodID), string(body))
}

func (s *Store) LoadProvider(providerID string, methodID string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !HasSecret() {
		return map[string]string{}, nil
	}
	payload, err := readEncryptedFile(s.ProviderPath(providerID, methodID))
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

func (s *Store) ProviderExists(providerID string, methodID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return fileExists(s.ProviderPath(providerID, methodID))
}

func (s *Store) SaveOAuthToken(providerID string, tokenID string, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return writeEncryptedFile(s.OAuthTokenPath(providerID, tokenID), value)
}

func (s *Store) LoadOAuthToken(providerID string, tokenID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return readEncryptedFile(s.OAuthTokenPath(providerID, tokenID))
}

func (s *Store) OAuthTokenExists(providerID string, tokenID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return fileExists(s.OAuthTokenPath(providerID, tokenID))
}

func (s *Store) DeleteOAuthToken(providerID string, tokenID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(s.OAuthTokenPath(providerID, tokenID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Store) ProviderPath(providerID string, methodID string) string {
	return filepath.Join(s.stateDir, providerID, fmt.Sprintf("%s_config.enc", methodID))
}

func (s *Store) OAuthTokenPath(providerID string, tokenID string) string {
	return filepath.Join(s.stateDir, providerID, fmt.Sprintf("%s.enc", tokenID))
}

func writeEncryptedFile(path string, value string) error {
	secret := Secret()
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
	secret := Secret()
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

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
