package anonymizer

import "fmt"

// TokenStoreFactory creates per-run stores used by the anonymizer.
type TokenStoreFactory interface {
	NewRunStore() (RunTokenStore, error)
	Close() error
}

// RunTokenStore persists and resolves token mappings for one anonymizer run.
type RunTokenStore interface {
	TokenFor(entityType EntityType, normalizedKey string, originalValue string) (string, error)
	ValueForToken(token string) (string, bool, error)
	Close() error
}

func newRunTokenStore(factory TokenStoreFactory) (RunTokenStore, error) {
	if factory == nil {
		return newMemoryRunTokenStore(), nil
	}

	return factory.NewRunStore()
}

type memoryRunTokenStore struct {
	tokens map[EntityType]map[string]string
	values map[string]string
	nextID map[EntityType]int
}

func newMemoryRunTokenStore() *memoryRunTokenStore {
	return &memoryRunTokenStore{
		tokens: make(map[EntityType]map[string]string),
		values: make(map[string]string),
		nextID: make(map[EntityType]int),
	}
}

func (s *memoryRunTokenStore) TokenFor(entityType EntityType, normalizedKey string, originalValue string) (string, error) {
	if s.tokens[entityType] == nil {
		s.tokens[entityType] = make(map[string]string)
	}
	if token, ok := s.tokens[entityType][normalizedKey]; ok {
		return token, nil
	}

	s.nextID[entityType]++
	token := fmt.Sprintf("[%s_%d]", entityType, s.nextID[entityType])
	s.tokens[entityType][normalizedKey] = token
	s.values[token] = originalValue
	return token, nil
}

func (s *memoryRunTokenStore) ValueForToken(token string) (string, bool, error) {
	value, ok := s.values[token]
	return value, ok, nil
}

func (s *memoryRunTokenStore) Close() error {
	return nil
}
